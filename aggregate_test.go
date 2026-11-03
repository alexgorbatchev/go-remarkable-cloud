package cloud_test

import (
	"context"
	"net/http/httptest"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestListItemsAggregate(t *testing.T) {
	for _, layout := range []string{"nested", "flat", "empty"} {
		t.Run(layout, func(t *testing.T) {
			blobs := blobFixture{}
			docMeta := blobs.add("doc.metadata", `{"visibleName":"2026 - Daily","type":"DocumentType","parent":"folder","lastModified":"1790799655380"}`)
			folderMeta := blobs.add("folder.metadata", `{"visibleName":"Plans","type":"CollectionType"}`)
			var entries []cloud.SchemaEntry
			switch layout {
			case "nested":
				entries = []cloud.SchemaEntry{blobs.manifest("doc", false, docMeta), blobs.manifest("folder", false, folderMeta)}
			case "flat":
				entries = []cloud.SchemaEntry{docMeta, folderMeta}
			}
			root := blobs.manifest("root", true, entries...)
			// Root aggregate records are not document entries.
			root = blobs.add("root", "4\n0:.:75:211191896\n"+blobs[root.Hash])
			server := httptest.NewServer(blobs.handler(t, root.Hash))

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
