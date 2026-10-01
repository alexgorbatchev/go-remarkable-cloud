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
	"strings"
	"sync"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

type snapshotServer struct {
	t          *testing.T
	mu         sync.Mutex
	root       cloud.RootState
	initial    cloud.RootState
	blobs      map[string][]byte
	docHash    string
	sourceHash string
	puts       int
	commits    int
	race       string
}

func newSnapshotServer(t *testing.T, race string) (*snapshotServer, *cloud.Client) {
	t.Helper()
	pdf, ink, content := []byte("PDF background"), []byte("native ink"), []byte(`{"tags":[]}`)
	doc := []byte(fmt.Sprintf("3\n%s:0:doc.pdf:0:%d\n%s:0:doc/page.rm:0:%d\n%s:0:doc.content:0:%d\n", blobHash(pdf), len(pdf), blobHash(ink), len(ink), blobHash(content), len(content)))
	source := []byte("source snapshot")
	root := []byte(fmt.Sprintf("4\n0:.:2:100\n%s:0:doc:3:50\n%s:0:source:1:50\n", blobHash(doc), blobHash(source)))
	s := &snapshotServer{t: t, root: cloud.RootState{Hash: blobHash(root), Generation: 5, SchemaVersion: 4}, docHash: blobHash(doc), sourceHash: blobHash(source), race: race,
		blobs: map[string][]byte{blobHash(root): root, blobHash(doc): doc, blobHash(source): source, blobHash(pdf): pdf, blobHash(ink): ink, blobHash(content): content}}
	s.initial = s.root
	server := httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(server.Close)
	c, err := cloud.NewClient(cloud.WithStorageHost(server.URL), cloud.WithConfig(&cloud.Config{UserToken: "test"}))
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func (s *snapshotServer) changeSource() {
	data := bytes.ReplaceAll(s.blobs[s.root.Hash], []byte(s.sourceHash), []byte(blobHash([]byte("changed source"))))
	s.root.Hash = blobHash(data)
	s.root.Generation++
	s.blobs[s.root.Hash] = data
}

func (s *snapshotServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.URL.Path == "/sync/v3/root" {
		if s.race == "root-read" {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.Method == http.MethodPut {
			s.commits++
			var update struct {
				Hash       string `json:"hash"`
				Generation int64  `json:"generation"`
			}
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				s.t.Error(err)
			}
			if update.Generation != s.initial.Generation {
				s.t.Error("commit did not use caller preflight generation")
			}
			if update.Generation != s.root.Generation {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			s.root.Hash = update.Hash
			s.root.Generation++
			if s.race == "unknown" {
				if _, err := fmt.Fprint(w, "invalid commit response"); err != nil {
					s.t.Error(err)
				}
				return
			}
		}
		if err := json.NewEncoder(w).Encode(s.root); err != nil {
			s.t.Error(err)
		}
		return
	}
	hash := strings.TrimPrefix(r.URL.Path, "/sync/v3/files/")
	if r.Method == http.MethodPut {
		s.puts++
		if s.race == "during-upload" && s.puts == 1 {
			s.changeSource()
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			s.t.Error(err)
		}
		s.blobs[hash] = data
		return
	}
	data, ok := s.blobs[hash]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if s.race == "postverify" && s.commits > 0 && r.Header.Get("rm-filename") == "doc.content" {
		data = []byte("corrupt after commit")
	}
	if _, err := w.Write(data); err != nil {
		s.t.Error(err)
	}
}

func TestUpdateDocumentFilesAtRoot(t *testing.T) {
	for _, race := range []string{"", "source-before-update", "same-hash-new-generation", "hash-only-change", "during-upload", "root-read", "unknown", "postverify", "destination-hash"} {
		t.Run(race, func(t *testing.T) {
			s, c := newSnapshotServer(t, race)
			opts := cloud.UpdateDocumentOptions{ID: "doc", ExpectedHash: s.docHash, ExpectedRoot: s.initial,
				Files: []cloud.FileUpdate{{Name: "doc.content", Data: []byte(`{"tags":[{"name":"copied"}]}`)}}}
			switch race {
			case "source-before-update":
				s.changeSource()
			case "same-hash-new-generation":
				s.root.Generation++
			case "hash-only-change":
				s.changeSource()
				s.root.Generation = s.initial.Generation
			case "destination-hash":
				opts.ExpectedHash = blobHash([]byte("other document"))
			}
			before := s.root
			result, err := c.UpdateDocumentFilesAtRoot(context.Background(), opts)
			want := cloud.UpdateStaged
			switch race {
			case "":
				want = cloud.UpdateVerified
			case "unknown":
				want = cloud.UpdateCommitUnknown
			case "postverify":
				want = cloud.UpdateCommitted
			}
			if (err != nil) != (race != "") || result.State != want {
				t.Fatalf("result %+v, err %v; want state %s", result, err, want)
			}
			if race == "source-before-update" || race == "same-hash-new-generation" || race == "hash-only-change" || race == "during-upload" {
				if !errors.Is(err, cloud.ErrGenerationConflict) {
					t.Fatalf("missing generation conflict: %v", err)
				}
			}
			if want == cloud.UpdateStaged && race != "during-upload" && (s.puts != 0 || s.commits != 0 || s.root != before) {
				t.Fatalf("preflight failure wrote cloud data: puts %d, commits %d, root %+v", s.puts, s.commits, s.root)
			}
			if race == "during-upload" && (s.commits != 1 || len(result.Uploaded) != 1 || s.root.Generation != s.initial.Generation+1) {
				t.Fatalf("staging race lost partial state or committed: %+v, root %+v", result, s.root)
			}
			if race != "" {
				return
			}
			root, err := cloud.ParseManifest(s.root.Hash, bytes.NewReader(s.blobs[s.root.Hash]))
			if err != nil {
				t.Fatal(err)
			}
			if root.Find("source").Hash != s.sourceHash || result.RootHash != s.root.Hash {
				t.Fatal("source snapshot or confirmed root changed")
			}
			doc, err := cloud.ParseManifest(root.Find("doc").Hash, bytes.NewReader(s.blobs[root.Find("doc").Hash]))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"doc.pdf", "doc/page.rm"} {
				original, err := cloud.ParseManifest(s.docHash, bytes.NewReader(s.blobs[s.docHash]))
				if err != nil {
					t.Fatal(err)
				}
				if doc.Find(name).Hash != original.Find(name).Hash {
					t.Fatalf("unchanged file reference lost: %s", name)
				}
			}
		})
	}
}

func TestUpdateDocumentFilesAtRootInvalidSnapshot(t *testing.T) {
	for _, invalid := range []string{"missing-hash", "invalid-hash", "negative-generation"} {
		t.Run(invalid, func(t *testing.T) {
			s, c := newSnapshotServer(t, "")
			opts := cloud.UpdateDocumentOptions{ID: "doc", ExpectedHash: s.docHash, ExpectedRoot: s.initial, Files: []cloud.FileUpdate{{Name: "doc.content", Data: []byte("content")}}}
			switch invalid {
			case "missing-hash":
				opts.ExpectedRoot.Hash = ""
			case "invalid-hash":
				opts.ExpectedRoot.Hash = "invalid"
			case "negative-generation":
				opts.ExpectedRoot.Generation = -1
			}
			result, err := c.UpdateDocumentFilesAtRoot(context.Background(), opts)
			if err == nil || result.State != cloud.UpdateStaged || s.puts != 0 || s.commits != 0 {
				t.Fatalf("invalid snapshot accepted or wrote data: %+v, %v", result, err)
			}
		})
	}
}
