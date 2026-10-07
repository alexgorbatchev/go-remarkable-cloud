package cloud_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestResolverMetadataErrors(t *testing.T) {
	for _, layout := range []string{"nested", "flat"} {
		for _, failure := range []string{"download", "json", "manifest"} {
			if layout == "flat" && failure == "manifest" {
				continue
			}
			for _, operation := range []string{"list", "id", "resolve", "name", "path"} {
				t.Run(layout+"/"+failure+"/"+operation, func(t *testing.T) {
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						switch r.URL.Path {
						case "/sync/v3/root":
							if _, err := w.Write([]byte(`{"hash":"root"}`)); err != nil {
								t.Error(err)
							}
						case "/sync/v3/files/root":
							line := "schema:doc:0:1\n"
							if layout == "flat" {
								line = "meta:doc.metadata:0:1\n"
							}
							if _, err := w.Write([]byte(line)); err != nil {
								t.Error(err)
							}
						case "/sync/v3/files/schema":
							if failure == "manifest" {
								w.WriteHeader(http.StatusServiceUnavailable)
								return
							}
							if _, err := w.Write([]byte("meta:doc.metadata:0:1\n")); err != nil {
								t.Error(err)
							}
						case "/sync/v3/files/meta":
							if failure == "download" {
								w.WriteHeader(http.StatusServiceUnavailable)
								return
							}
							if _, err := w.Write([]byte(`{"visibleName":`)); err != nil {
								t.Error(err)
							}
						default:
							w.WriteHeader(http.StatusNotFound)
						}
					}))
					defer s.Close()
					c, err := cloud.NewClient(cloud.WithStorageHost(s.URL), cloud.WithConfig(&cloud.Config{UserToken: "test"}))
					if err != nil {
						t.Fatal(err)
					}
					ctx := context.Background()
					switch operation {
					case "list":
						_, err = c.ListItems(ctx)
					case "id":
						_, err = c.ResolveByID(ctx, "doc")
					case "resolve":
						_, err = c.Resolve(ctx, "doc")
					case "name":
						_, err = c.ResolveByName(ctx, "Title")
					case "path":
						_, err = c.ResolveByPath(ctx, "Folder/Title")
					}
					if err == nil || !strings.Contains(err.Error(), "doc") {
						t.Fatalf("expected contextual document error, got %v", err)
					}
				})
			}
		}
	}
}

// TestItemGetContentManifestFailure reads .content of an item in a flattened root, whose root entries
// list only the item's own .docSchema and .metadata, so the .content entry is reachable only through
// the item manifest. A failed manifest download keeps its own classification; only a manifest that
// was read and lists no .content reports ErrItemNotFound.
func TestItemGetContentManifestFailure(t *testing.T) {
	tests := []struct {
		name     string
		schema   cannedResponse
		status   int
		sentinel error
		wantText string
	}{
		{
			name:     "manifest 502",
			schema:   cannedResponse{http.StatusBadGateway, "upstream unavailable"},
			status:   http.StatusBadGateway,
			wantText: "get manifest for content of doc: get blob schema (doc.docSchema) failed with status 502: upstream unavailable",
		},
		{
			name:     "manifest 401",
			schema:   cannedResponse{http.StatusUnauthorized, "expired"},
			status:   http.StatusUnauthorized,
			sentinel: cloud.ErrUnauthorized,
			wantText: "get manifest for content of doc: unauthorized: missing or invalid credentials: get blob schema (doc.docSchema) failed with status 401: expired",
		},
		{
			name:     "manifest without content",
			schema:   cannedResponse{http.StatusOK, "3\npdf:0:doc.pdf:0:1\n"},
			sentinel: cloud.ErrItemNotFound,
			wantText: "item not found: content entry not found for item doc",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newCannedClient(t, map[string]cannedResponse{
				rootPath:                {http.StatusOK, `{"hash":"root","generation":1}`},
				"/sync/v3/files/root":   {http.StatusOK, "3\nschema:0:doc.docSchema:0:1\nmeta:0:doc.metadata:0:1\n"},
				"/sync/v3/files/meta":   {http.StatusOK, `{"visibleName":"Doc","type":"DocumentType","parent":""}`},
				"/sync/v3/files/schema": tt.schema,
			}, cloud.Config{UserToken: "user-token"}, cloud.WithAutoRenew(false))
			items, err := c.ListItems(context.Background())
			if err != nil || len(items) != 1 || items[0].ID != "doc" {
				t.Fatalf("expected the flattened item doc, got %v, %v", items, err)
			}
			_, err = items[0].GetContent(context.Background())
			var statusErr *cloud.StatusError
			if got := errors.As(err, &statusErr); got != (tt.status != 0) || got && statusErr.StatusCode != tt.status {
				t.Fatalf("expected manifest status %d, got %v", tt.status, err)
			}
			for _, sentinel := range classificationSentinels {
				if got, want := errors.Is(err, sentinel), sentinel == tt.sentinel; got != want {
					t.Fatalf("errors.Is(err, %q) = %v, want %v (err: %v)", sentinel, got, want, err)
				}
			}
			if err.Error() != tt.wantText {
				t.Fatalf("error text mismatch\n got: %q\nwant: %q", err.Error(), tt.wantText)
			}
		})
	}
}
