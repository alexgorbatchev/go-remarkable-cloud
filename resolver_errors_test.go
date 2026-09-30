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
