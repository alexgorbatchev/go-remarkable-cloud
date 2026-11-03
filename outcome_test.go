package cloud_test

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestRejectedCommitRemainsStaged(t *testing.T) {
	for _, operation := range []string{"create", "update"} {
		for _, tc := range []struct{ status, renewal int }{{400, 0}, {401, 0}, {401, 200}, {401, 401}, {401, 503}, {403, 200}, {403, 403}, {409, 0}, {412, 0}, {429, 0}, {503, 0}} {
			t.Run(fmt.Sprintf("%s/%d/renew%d", operation, tc.status, tc.renewal), func(t *testing.T) {
				var handler http.HandlerFunc
				var call func(*cloud.Client) (cloud.UpdateState, error)
				var unchanged func() bool
				if operation == "create" {
					s, _ := newCreationServer(t, "")
					handler = s.serveHTTP
					unchanged = func() bool { return s.root == s.initial }
					call = func(c *cloud.Client) (cloud.UpdateState, error) {
						result, err := c.CreateDocument(t.Context(), cloud.CreateDocumentOptions{ID: "created", ExpectedRoot: s.initial, Files: creationFiles()})
						return result.State, err
					}
				} else {
					s, _ := newSnapshotServer(t, "")
					handler = s.serveHTTP
					unchanged = func() bool { return s.root == s.initial }
					call = func(c *cloud.Client) (cloud.UpdateState, error) {
						result, err := c.UpdateDocumentFilesAtRoot(t.Context(), cloud.UpdateDocumentOptions{ID: "doc", ExpectedRoot: s.initial, ExpectedHash: s.docHash, Files: []cloud.FileUpdate{{Name: "doc.content", Data: []byte(`{"tags":[]}`)}}})
						return result.State, err
					}
				}
				puts, renewals := 0, 0
				intercept := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token/json/2/user/new" {
						renewals++
						w.WriteHeader(tc.renewal)
						_, _ = w.Write([]byte("test"))
						return
					}
					if r.URL.Path == "/sync/v3/root" && r.Method == http.MethodPut {
						puts++
						w.WriteHeader(tc.status)
						_, _ = w.Write([]byte("token count exceeded"))
						return
					}
					handler(w, r)
				})
				c, err := cloud.NewClient(cloud.WithHTTPClient(&http.Client{Transport: handlerTransport{intercept}}), cloud.WithStorageHost("https://cloud.test"), cloud.WithAuthBaseURL("https://cloud.test"), cloud.WithAutoRenew(tc.renewal != 0), cloud.WithConfig(&cloud.Config{UserToken: "test", DeviceToken: "device"}))
				if err != nil {
					t.Fatal(err)
				}
				state, err := call(c)
				wantState := cloud.UpdateStaged
				if tc.status >= 500 {
					wantState = cloud.UpdateCommitUnknown
				}
				if err == nil || state != wantState || !unchanged() {
					t.Fatalf("state=%s, err=%v, unchanged=%v", state, err, unchanged())
				}
				if wantState == cloud.UpdateStaged && strings.Contains(err.Error(), "outcome unknown") {
					t.Fatalf("definite refusal described as unknown: %v", err)
				}
				wantPuts, wantRenewals := 1, 0
				if tc.renewal != 0 {
					wantRenewals = 1
				}
				if tc.renewal == 200 {
					wantPuts = 2
				}
				if puts != wantPuts || renewals != wantRenewals {
					t.Fatalf("puts=%d renewals=%d, want %d/%d", puts, renewals, wantPuts, wantRenewals)
				}
				var statusErr *cloud.StatusError
				if !errors.As(err, &statusErr) {
					t.Fatalf("lost status classification: %v", err)
				}
				wantStatus := tc.status
				if tc.renewal >= 400 {
					wantStatus = tc.renewal
				}
				if statusErr.StatusCode != wantStatus {
					t.Fatalf("status=%d want=%d", statusErr.StatusCode, wantStatus)
				}
			})
		}
	}
}

func TestContentManifestFailureKeepsCause(t *testing.T) {
	for _, status := range []int{401, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			c := newCannedClient(t, map[string]cannedResponse{blobPath: {status, "manifest unavailable"}}, cloud.Config{UserToken: "test"}, cloud.WithAutoRenew(false))
			item := &cloud.Item{ID: "doc", Entries: []cloud.SchemaEntry{{ID: "doc.docSchema", Hash: fixtureHash("missing")}}}
			item.BindClient(c)
			_, err := item.GetContent(t.Context())
			var statusErr *cloud.StatusError
			if errors.Is(err, cloud.ErrItemNotFound) || !errors.As(err, &statusErr) || statusErr.StatusCode != status {
				t.Fatalf("lost manifest failure: %v", err)
			}
		})
	}
}

func TestBadRequestDoesNotRenew(t *testing.T) {
	for _, body := range []string{"token count exceeded", "invalid rm-filename token", "Token expired"} {
		t.Run(body, func(t *testing.T) {
			c := newCannedClient(t, map[string]cannedResponse{rootPath: {400, body}}, cloud.Config{UserToken: "test", DeviceToken: "device"})
			_, err := c.GetRootState(t.Context())
			var statusErr *cloud.StatusError
			if !errors.As(err, &statusErr) || statusErr.StatusCode != 400 || statusErr.Body != body {
				t.Fatalf("lost bad request: %v", err)
			}
		})
	}
}
