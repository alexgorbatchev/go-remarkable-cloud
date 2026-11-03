package cloud_test

import (
	"context"
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
					blobs := blobFixture{}
					meta := blobs.add("doc.metadata", `{"visibleName":`)
					schema := blobs.manifest("doc", false, meta)
					entry := schema
					if layout == "flat" {
						entry = meta
					}
					root := blobs.manifest("root", true, entry)
					handler := blobs.handler(t, root.Hash)
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if (failure == "manifest" && r.URL.Path == "/sync/v3/files/"+schema.Hash) ||
							(failure == "download" && r.URL.Path == "/sync/v3/files/"+meta.Hash) {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						handler(w, r)
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
