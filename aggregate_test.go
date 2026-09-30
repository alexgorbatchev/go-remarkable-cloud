package cloud_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestListItemsAggregate(t *testing.T) {
	for _, layout := range []string{"nested", "flat", "empty"} {
		t.Run(layout, func(t *testing.T) {
			root := "4\n0:.:75:211191896\n"
			switch layout {
			case "nested":
				root += "doc-schema:doc:0:100\nfolder-schema:folder:0:100\n"
			case "flat":
				root += "doc-meta:doc.metadata:0:100\nfolder-meta:folder.metadata:0:100\n"
			}
			blobs := map[string]string{
				"root":          root,
				"doc-schema":    "doc-meta:doc.metadata:0:100\n",
				"folder-schema": "folder-meta:folder.metadata:0:100\n",
				"doc-meta":      `{"visibleName":"2026 - Daily","type":"DocumentType","parent":"folder","lastModified":"1790799655380"}`,
				"folder-meta":   `{"visibleName":"Plans","type":"CollectionType"}`,
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/sync/v3/root" {
					fmt.Fprint(w, `{"hash":"root","schemaVersion":4}`)
					return
				}
				for hash, blob := range blobs {
					if r.URL.Path == "/sync/v3/files/"+hash {
						fmt.Fprint(w, blob)
						return
					}
				}
				t.Errorf("unexpected request: %s", r.URL.Path)
				http.NotFound(w, r)
			}))
			t.Cleanup(server.Close)
			client, err := cloud.NewClient(cloud.WithStorageHost(server.URL), cloud.WithConfig(&cloud.Config{UserToken: "test"}))
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			items, err := client.ListItems(ctx)
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if layout == "empty" {
				want = 0
			}
			if len(items) != want {
				t.Fatalf("got %d items, want %d: %+v", len(items), want, items)
			}
			if layout == "empty" {
				return
			}
			byID := make(map[string]*cloud.Item)
			for _, item := range items {
				byID[item.ID] = item
			}
			doc, folder := byID["doc"], byID["folder"]
			if doc == nil || folder == nil {
				t.Fatalf("missing items: %+v", byID)
			}
			if doc.Metadata.VisibleName != "2026 - Daily" || doc.Metadata.LastModified != "1790799655380" || doc.Metadata.Parent != "folder" || !doc.IsDocument() {
				t.Fatalf("wrong document metadata: %+v", doc.Metadata)
			}
			if folder.Metadata.VisibleName != "Plans" || !folder.IsCollection() {
				t.Fatalf("wrong folder metadata: %+v", folder.Metadata)
			}
			resolved, err := client.ResolveByName(ctx, "2026 - Daily")
			if err != nil || resolved.ID != "doc" {
				t.Fatalf("title resolution: %v, %+v", err, resolved)
			}
		})
	}
}
