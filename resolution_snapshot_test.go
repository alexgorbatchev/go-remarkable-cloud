package cloud_test

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestResolveUsesOneSnapshot(t *testing.T) {
	for _, layout := range []string{"flat", "nested"} {
		for _, query := range []string{"doc", "Note", "Missing", "Work/Note", "Q3/Q4 plan"} {
			t.Run(layout+"/"+query, func(t *testing.T) {
				blobs := blobFixture{}
				folder := blobs.add("folder.metadata", `{"visibleName":"Work","type":"CollectionType"}`)
				doc := blobs.add("doc.metadata", `{"visibleName":"Note","type":"DocumentType","parent":"folder"}`)
				slash := blobs.add("slash.metadata", `{"visibleName":"Q3/Q4 plan","type":"DocumentType"}`)
				entries := []cloud.SchemaEntry{folder, doc, slash}
				if layout == "nested" {
					for i, e := range entries {
						entries[i] = blobs.manifest(strings.TrimSuffix(e.ID, ".metadata"), false, e)
					}
				}
				root := blobs.manifest("root", true, entries...)
				handler := blobs.handler(t, root.Hash)
				var mu sync.Mutex
				counts := map[string]int{}
				c, err := cloud.NewClient(cloud.WithStorageHost("https://cloud.test"), cloud.WithConfig(&cloud.Config{UserToken: "test"}), cloud.WithHTTPClient(&http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					counts[r.URL.Path]++
					mu.Unlock()
					handler(w, r)
				})}}))
				if err != nil {
					t.Fatal(err)
				}
				item, err := c.Resolve(t.Context(), query)
				if query == "Missing" {
					if !errors.Is(err, cloud.ErrItemNotFound) || !strings.Contains(err.Error(), "name Missing") {
						t.Fatalf("wrong missing-name error: %v", err)
					}
				} else {
					want := "doc"
					if query == "Q3/Q4 plan" {
						want = "slash"
					}
					if err != nil || item.ID != want {
						t.Fatalf("Resolve=%+v, %v", item, err)
					}
				}
				for path, n := range counts {
					if n != 1 {
						t.Errorf("%s fetched %d times", path, n)
					}
				}
				if counts["/sync/v3/root"] != 1 {
					t.Fatalf("root requests=%d", counts["/sync/v3/root"])
				}
			})
		}
	}
}

func TestResolveByIDNestedMissDoesNotList(t *testing.T) {
	blobs := blobFixture{}
	entry := blobs.manifest("doc", false, blobs.add("doc.metadata", `{"visibleName":"Note"}`))
	root := blobs.manifest("root", true, entry)
	c := newCannedClient(t, map[string]cannedResponse{
		rootPath:                      {200, `{"hash":"` + root.Hash + `"}`},
		"/sync/v3/files/" + root.Hash: {200, blobs[root.Hash]},
	}, cloud.Config{UserToken: "test"})
	_, err := c.ResolveByID(t.Context(), "absent")
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Fatalf("expected ID miss without listing: %v", err)
	}
}

func TestTrashedAncestorsAreExcluded(t *testing.T) {
	items := []listingItem{
		{id: "trash-folder", name: "Old", folder: true, parent: "trash"},
		{id: "nested", name: "Nested", folder: true, parent: "trash-folder"},
		{id: "deleted-folder", name: "Deleted", folder: true, deleted: true},
		{id: "trashed-child", name: "Note", parent: "nested"},
		{id: "deleted-child", name: "Note", parent: "deleted-folder"},
		{id: "live", name: "Note"},
	}
	for _, layout := range []string{"nested", "flat"} {
		t.Run(layout, func(t *testing.T) {
			c := newShuffledListingClient(t, layout, items, 1)
			got, err := c.ListItems(t.Context())
			if err != nil || len(got) != 1 || got[0].ID != "live" {
				t.Fatalf("live listing=%+v, %v", got, err)
			}
			item, err := c.ResolveByName(t.Context(), "Note")
			if err != nil || item.ID != "live" {
				t.Fatalf("name resolution=%+v, %v", item, err)
			}
			all, err := c.ListItems(t.Context(), cloud.WithIncludeDeleted(true))
			if err != nil || len(all) != len(items) {
				t.Fatalf("deleted-inclusive listing=%d, %v", len(all), err)
			}
		})
	}
}

func TestRootFailureNamesOperationOnce(t *testing.T) {
	c := newCannedClient(t, map[string]cannedResponse{rootPath: {500, "boom"}}, cloud.Config{UserToken: "test"})
	_, err := c.ListItems(t.Context())
	if err == nil || strings.Count(err.Error(), "get root state") != 1 {
		t.Fatalf("redundant operation context: %v", err)
	}
}
