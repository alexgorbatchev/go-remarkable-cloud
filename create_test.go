package cloud_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

type creationServer struct {
	t        *testing.T
	mu       sync.Mutex
	root     cloud.RootState
	initial  cloud.RootState
	blobs    map[string][]byte
	puts     int
	commits  int
	renewals int
	failure  string
}

// commitResponses maps a creation server failure to the answer for each root commit attempt in turn.
var commitResponses = map[string][]cannedResponse{
	"conflict":               {{http.StatusPreconditionFailed, "generation changed"}},
	"conflict-409":           {{http.StatusConflict, "generation changed"}},
	"commit-error":           {{http.StatusServiceUnavailable, "commit unavailable"}},
	"commit-401":             {{http.StatusUnauthorized, "expired"}, {http.StatusUnauthorized, "expired"}},
	"commit-401-renew-fails": {{http.StatusUnauthorized, "expired"}},
	"commit-403":             {{http.StatusForbidden, "forbidden"}, {http.StatusForbidden, "forbidden"}},
	"commit-400":             {{http.StatusBadRequest, `{"message":"invalid rm-filename token"}`}},
	"commit-400-malformed":   {{http.StatusBadRequest, malformedTokenBody}, {http.StatusBadRequest, malformedTokenBody}},
	"commit-401-then-503":    {{http.StatusUnauthorized, "expired"}, {http.StatusServiceUnavailable, "commit unavailable"}},
}

func newCreationServer(t *testing.T, failure string) (*creationServer, *cloud.Client) {
	t.Helper()
	data := []byte(fmt.Sprintf("4\n0:.:1:4\n%s:0:existing:1:4\n", blobHash([]byte("existing"))))
	s := &creationServer{t: t, root: cloud.RootState{Hash: blobHash(data), Generation: 5, SchemaVersion: 4}, blobs: map[string][]byte{blobHash(data): data}, failure: failure}
	s.initial = s.root
	server := httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(server.Close)
	cache := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cache, "blobs"), 0700); err != nil {
		t.Fatal(err)
	}
	for hash, data := range s.blobs {
		if err := os.WriteFile(filepath.Join(cache, "blobs", hash), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range creationFiles() {
		if err := os.WriteFile(filepath.Join(cache, "blobs", blobHash(file.Data)), file.Data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	c, err := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithAuthBaseURL(server.URL),
		cloud.WithSaveOnRenew(false),
		cloud.WithConfig(&cloud.Config{DeviceToken: "device", UserToken: "test"}),
		cloud.WithCacheDir(cache),
	)
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func (s *creationServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == renewPath {
		s.serveRenewal(w)
		return
	}
	if r.Header.Get("Authorization") != "Bearer test" {
		s.t.Error("missing auth")
	}
	if r.URL.Path == "/sync/v3/root" {
		s.serveRoot(w, r)
		return
	}
	hash := strings.TrimPrefix(r.URL.Path, "/sync/v3/files/")
	name := r.Header.Get("rm-filename")
	if r.Method == http.MethodPut {
		s.puts++
		if s.failure == "upload" || (s.failure == "document-upload" && name == "created.docSchema") || (s.failure == "root-upload" && name == "root.docSchema") {
			w.WriteHeader(http.StatusBadGateway)
			if _, err := fmt.Fprint(w, "upload unavailable"); err != nil {
				s.t.Error(err)
			}
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			s.t.Error(err)
		}
		if name == "" || r.Header.Get("x-goog-hash") == "" || r.ContentLength != int64(len(data)) {
			s.t.Error("invalid blob upload headers")
		}
		s.blobs[hash] = data
		return
	}
	if s.failure == "manifest-read" && hash == s.initial.Hash {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	data, ok := s.blobs[hash]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if s.failure == "stage-bytes" && name == "created.pdf" || s.failure == "post-bytes" && s.commits > 0 && name == "created.pdf" {
		data = []byte("corrupt")
	}
	if s.failure == "file-association" && s.commits > 0 && name == "created.docSchema" {
		data = bytes.ReplaceAll(data, []byte("created.pdf"), []byte("created.missing"))
	}
	if _, err := w.Write(data); err != nil {
		s.t.Error(err)
	}
}

func (s *creationServer) serveRoot(w http.ResponseWriter, r *http.Request) {
	if s.failure == "root-read" || s.failure == "post-root-read" && s.commits > 0 && r.Method == http.MethodGet {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if r.Method == http.MethodPut {
		s.commits++
		if responses, ok := commitResponses[s.failure]; ok {
			if s.commits > len(responses) {
				s.t.Errorf("unexpected root commit attempt %d", s.commits)
				w.WriteHeader(http.StatusTeapot)
				return
			}
			response := responses[s.commits-1]
			w.WriteHeader(response.status)
			if _, err := fmt.Fprint(w, response.body); err != nil {
				s.t.Error(err)
			}
			return
		}
		var update struct {
			Hash       string `json:"hash"`
			Generation int64  `json:"generation"`
			Broadcast  bool   `json:"broadcast"`
		}
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			s.t.Error(err)
		}
		if update.Generation != s.initial.Generation || !update.Broadcast || r.Header.Get("rm-filename") != "roothash" {
			s.t.Error("invalid generation-checked root commit")
		}
		s.root.Hash = update.Hash
		s.root.Generation++
		if s.failure == "unknown" {
			if _, err := fmt.Fprint(w, "invalid JSON"); err != nil {
				s.t.Error(err)
			}
			return
		}
	}
	root := s.root
	if s.failure == "association" && s.commits > 0 && r.Method == http.MethodGet {
		root.Hash = s.initial.Hash
	}
	if err := json.NewEncoder(w).Encode(root); err != nil {
		s.t.Error(err)
	}
}

// serveRenewal mints the same user token the server accepts, or rejects the device token.
func (s *creationServer) serveRenewal(w http.ResponseWriter) {
	s.renewals++
	if s.failure == "commit-401-renew-fails" {
		w.WriteHeader(http.StatusUnauthorized)
		if _, err := fmt.Fprint(w, "device token revoked"); err != nil {
			s.t.Error(err)
		}
		return
	}
	if _, err := fmt.Fprint(w, "test"); err != nil {
		s.t.Error(err)
	}
}

func creationFiles() []cloud.FileUpdate {
	return []cloud.FileUpdate{
		{Name: "created.pdf", Data: []byte("%PDF-1.7\nmultiple pages and embedded links\x00\xff\n%%EOF")},
		{Name: "created.metadata", Data: []byte(`{"visibleName":"Planner","parent":"folder","type":"DocumentType"}`)},
		{Name: "created.content", Data: []byte(`{"fileType":"pdf","pageCount":2,"originalPageCount":2}`)},
		{Name: "created.pagedata", Data: []byte("Blank\nBlank\n")},
	}
}

func TestCreateDocument(t *testing.T) {
	tests := []struct {
		failure string
		state   cloud.UpdateState
	}{
		{"", cloud.UpdateVerified}, {"stale-hash", cloud.UpdateStaged}, {"stale-generation", cloud.UpdateStaged},
		{"existing", cloud.UpdateStaged}, {"root-read", cloud.UpdateStaged}, {"manifest-read", cloud.UpdateStaged},
		{"upload", cloud.UpdateStaged}, {"document-upload", cloud.UpdateStaged}, {"root-upload", cloud.UpdateStaged},
		{"stage-bytes", cloud.UpdateStaged}, {"conflict", cloud.UpdateStaged}, {"unknown", cloud.UpdateCommitUnknown},
		{"association", cloud.UpdateCommitted}, {"file-association", cloud.UpdateCommitted}, {"post-bytes", cloud.UpdateCommitted},
		{"post-root-read", cloud.UpdateCommitted}, {"commit-error", cloud.UpdateCommitUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.failure, func(t *testing.T) {
			s, c := newCreationServer(t, tt.failure)
			opts := cloud.CreateDocumentOptions{ID: "created", ExpectedRoot: s.initial, Files: creationFiles()}
			switch tt.failure {
			case "stale-hash":
				opts.ExpectedRoot.Hash = blobHash([]byte("stale"))
			case "stale-generation":
				opts.ExpectedRoot.Generation--
			case "existing":
				opts.ID = "existing"
				opts.Files = []cloud.FileUpdate{{Name: "existing.pdf", Data: []byte("replacement")}}
			}
			var states []cloud.UpdateState
			opts.OnProgress = func(progress cloud.CreateResult) error {
				states = append(states, progress.State)
				if progress.ID != opts.ID || progress.DocumentHash == "" || progress.RootHash == "" || progress.Generation != s.initial.Generation {
					t.Fatalf("missing recovery identity before commit: %+v", progress)
				}
				if progress.State == cloud.UpdateCommitUnknown && s.commits != 0 {
					t.Fatal("uncertain commit evidence recorded too late")
				}
				return nil
			}
			result, err := c.CreateDocument(context.Background(), opts)
			if (err != nil) != (tt.failure != "") || result.State != tt.state {
				t.Fatalf("result = %+v, err = %v, want state %s", result, err, tt.state)
			}
			if tt.failure == "conflict" && !errors.Is(err, cloud.ErrGenerationConflict) {
				t.Fatalf("missing generation conflict sentinel: %v", err)
			}
			if strings.HasPrefix(tt.failure, "stale-") || tt.failure == "existing" {
				if s.puts != 0 || s.commits != 0 {
					t.Fatal("failed preflight wrote cloud data")
				}
			}
			if tt.failure != "" {
				return
			}
			if !reflect.DeepEqual(states, []cloud.UpdateState{cloud.UpdateStaged, cloud.UpdateCommitUnknown, cloud.UpdateCommitted, cloud.UpdateVerified}) || len(result.Uploaded) != len(opts.Files) {
				t.Fatalf("incomplete progress: %v, %+v", states, result)
			}
			manifest, err := cloud.ParseManifest(s.root.Hash, bytes.NewReader(s.blobs[s.root.Hash]))
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Find("existing") == nil || manifest.Find("created").Hash != result.DocumentHash || manifest.Find("created").Subfiles != len(opts.Files) {
				t.Fatal("creation lost existing item or document association")
			}
			for _, file := range opts.Files {
				if !bytes.Equal(s.blobs[blobHash(file.Data)], file.Data) {
					t.Fatalf("changed bytes: %s", file.Name)
				}
			}
		})
	}
}

// TestCreateDocumentCommitOutcome pins the state each root commit answer leaves: a 4xx final answer,
// or a 401 whose renewal fails before any retry, refused the commit and leaves it staged; a 5xx
// final answer may have been applied and leaves it commit-unknown.
func TestCreateDocumentCommitOutcome(t *testing.T) {
	tests := []struct {
		failure  string
		state    cloud.UpdateState
		op       string
		status   int
		sentinel error
		commits  int
		renewals int
		wantText string
	}{
		{
			failure: "commit-401", state: cloud.UpdateStaged, op: "commit root", status: http.StatusUnauthorized,
			sentinel: cloud.ErrUnauthorized, commits: 2, renewals: 1,
			wantText: "unauthorized: missing or invalid credentials: commit root failed with status 401: expired",
		},
		{
			failure: "commit-401-renew-fails", state: cloud.UpdateStaged, op: "renew user token", status: http.StatusUnauthorized,
			sentinel: cloud.ErrUnauthorized, commits: 1, renewals: 1,
			wantText: "root commit rejected: auth renewal failed after 401: unauthorized: missing or invalid credentials: renew user token failed with status 401: device token revoked",
		},
		{
			failure: "commit-403", state: cloud.UpdateStaged, op: "commit root", status: http.StatusForbidden,
			sentinel: cloud.ErrUnauthorized, commits: 2, renewals: 1,
			wantText: "unauthorized: missing or invalid credentials: commit root failed with status 403: forbidden",
		},
		{
			failure: "commit-400", state: cloud.UpdateStaged, op: "commit root", status: http.StatusBadRequest,
			commits: 1, renewals: 0,
			wantText: `commit root failed with status 400: {"message":"invalid rm-filename token"}`,
		},
		{
			failure: "commit-400-malformed", state: cloud.UpdateStaged, op: "commit root", status: http.StatusBadRequest,
			sentinel: cloud.ErrUnauthorized, commits: 2, renewals: 1,
			wantText: "unauthorized: missing or invalid credentials: commit root failed with status 400: " + malformedTokenBody,
		},
		{
			failure: "conflict", state: cloud.UpdateStaged, op: "commit root", status: http.StatusPreconditionFailed,
			sentinel: cloud.ErrGenerationConflict, commits: 1, renewals: 0,
			wantText: "cloud root generation conflict: commit root failed with status 412: generation changed",
		},
		{
			failure: "commit-401-then-503", state: cloud.UpdateCommitUnknown, op: "commit root", status: http.StatusServiceUnavailable,
			commits: 2, renewals: 1,
			wantText: "commit root failed with status 503: commit unavailable",
		},
		{
			failure: "commit-error", state: cloud.UpdateCommitUnknown, op: "commit root", status: http.StatusServiceUnavailable,
			commits: 1, renewals: 0,
			wantText: "commit root failed with status 503: commit unavailable",
		},
	}
	for _, tt := range tests {
		t.Run(tt.failure, func(t *testing.T) {
			s, c := newCreationServer(t, tt.failure)
			result, err := c.CreateDocument(context.Background(), cloud.CreateDocumentOptions{ID: "created", ExpectedRoot: s.initial, Files: creationFiles()})
			if result.State != tt.state {
				t.Fatalf("state = %s, want %s (err: %v)", result.State, tt.state, err)
			}
			var statusErr *cloud.StatusError
			if !errors.As(err, &statusErr) || statusErr.Op != tt.op || statusErr.StatusCode != tt.status {
				t.Fatalf("expected %s status %d, got %v", tt.op, tt.status, err)
			}
			for _, sentinel := range classificationSentinels {
				if got, want := errors.Is(err, sentinel), sentinel == tt.sentinel; got != want {
					t.Fatalf("errors.Is(err, %q) = %v, want %v (err: %v)", sentinel, got, want, err)
				}
			}
			if err.Error() != tt.wantText {
				t.Fatalf("error text mismatch\n got: %q\nwant: %q", err.Error(), tt.wantText)
			}
			if s.commits != tt.commits || s.renewals != tt.renewals {
				t.Fatalf("commits = %d, renewals = %d, want %d and %d", s.commits, s.renewals, tt.commits, tt.renewals)
			}
			if tt.state == cloud.UpdateStaged && s.root != s.initial {
				t.Fatalf("staged creation changed the cloud root from %+v to %+v", s.initial, s.root)
			}
		})
	}
}

func TestCreateDocumentInvalidInput(t *testing.T) {
	for _, invalid := range []string{"no-id", "bad-id", "no-root", "bad-root", "negative-generation", "no-files", "foreign", "duplicate", "delimiter"} {
		t.Run(invalid, func(t *testing.T) {
			s, c := newCreationServer(t, "")
			opts := cloud.CreateDocumentOptions{ID: "created", ExpectedRoot: s.initial, Files: creationFiles()}
			switch invalid {
			case "no-id":
				opts.ID = ""
			case "bad-id":
				opts.ID = "root\n"
			case "no-root":
				opts.ExpectedRoot.Hash = ""
			case "bad-root":
				opts.ExpectedRoot.Hash = "bad"
			case "negative-generation":
				opts.ExpectedRoot.Generation = -1
			case "no-files":
				opts.Files = nil
			case "foreign":
				opts.Files[0].Name = "other.pdf"
			case "duplicate":
				opts.Files = append(opts.Files, opts.Files[0])
			case "delimiter":
				opts.Files[0].Name = "created.pdf:bad"
			}
			if _, err := c.CreateDocument(context.Background(), opts); err == nil || s.puts != 0 || s.commits != 0 {
				t.Fatalf("invalid input accepted or wrote data: err %v, puts %d, commits %d", err, s.puts, s.commits)
			}
		})
	}
}

func TestCreateDocumentProgressFailure(t *testing.T) {
	for _, state := range []cloud.UpdateState{cloud.UpdateStaged, cloud.UpdateCommitUnknown, cloud.UpdateCommitted, cloud.UpdateVerified} {
		t.Run(string(state), func(t *testing.T) {
			s, c := newCreationServer(t, "")
			failure := errors.New("cannot persist recovery evidence")
			opts := cloud.CreateDocumentOptions{ID: "created", ExpectedRoot: s.initial, Files: creationFiles(), OnProgress: func(result cloud.CreateResult) error {
				if result.State == state {
					return failure
				}
				return nil
			}}
			result, err := c.CreateDocument(context.Background(), opts)
			want := state
			if state == cloud.UpdateCommitUnknown {
				want = cloud.UpdateStaged
			}
			if !errors.Is(err, failure) || result.State != want {
				t.Fatalf("lost progress failure: result %+v, err %v", result, err)
			}
			if (state == cloud.UpdateStaged || state == cloud.UpdateCommitUnknown) && s.commits != 0 {
				t.Fatal("committed before recording recovery evidence")
			}
		})
	}
}

func TestCreateDocumentProgressOwnership(t *testing.T) {
	for _, observe := range []bool{false, true} {
		t.Run(fmt.Sprintf("observer-%t", observe), func(t *testing.T) {
			s, c := newCreationServer(t, "")
			opts := cloud.CreateDocumentOptions{ID: "created", ExpectedRoot: s.initial, Files: creationFiles()}
			var beforeCommit cloud.CreateResult
			if observe {
				opts.OnProgress = func(progress cloud.CreateResult) error {
					if progress.State == cloud.UpdateCommitUnknown {
						beforeCommit = progress
						progress.Uploaded[0] = "observer changed its copy"
					}
					return nil
				}
			}
			result, err := c.CreateDocument(context.Background(), opts)
			if err != nil || result.State != cloud.UpdateVerified || result.Uploaded[0] != opts.Files[0].Name {
				t.Fatalf("observer changed upload result or nil observer failed: %+v, %v", result, err)
			}
			if observe && beforeCommit.State != cloud.UpdateCommitUnknown {
				t.Fatalf("retained precommit progress mutated: %+v", beforeCommit)
			}
		})
	}
}
