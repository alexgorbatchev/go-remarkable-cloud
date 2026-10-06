package cloud_test

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

// listingItem is one item served by newShuffledListingClient.
type listingItem struct {
	id      string
	name    string
	folder  bool
	parent  string
	deleted bool
}

// ambiguityFixture holds same-named items across root, nested, trashed, and duplicate-folder locations.
var ambiguityFixture = []listingItem{
	{id: "folder-work", name: "Work", folder: true},
	{id: "folder-proj-a", name: "Projects", folder: true, parent: "folder-work"},
	{id: "folder-proj-b", name: "Projects", folder: true, parent: "folder-work"},
	{id: "doc-projects", name: "Projects", parent: "folder-work"},
	{id: "folder-archive", name: "Archive", folder: true},
	{id: "folder-old", name: "Old", folder: true, parent: "trash"},
	{id: "folder-old-sub", name: "Sub", folder: true, parent: "folder-old"},
	{id: "doc-notes-root", name: "Notes"},
	{id: "doc-notes-proj", name: "Notes", parent: "folder-proj-a"},
	{id: "doc-notes-archive", name: "Notes", parent: "folder-archive"},
	{id: "doc-notes-old", name: "Notes", parent: "folder-old"},
	{id: "doc-notes-old-sub", name: "Notes", parent: "folder-old-sub"},
	{id: "doc-notes-trashed", name: "Notes", parent: "trash"},
	{id: "doc-notes-deleted", name: "Notes", deleted: true},
	{id: "doc-dup-b", name: "Dup", parent: "folder-archive"},
	{id: "doc-dup-a", name: "Dup", parent: "folder-archive"},
	{id: "doc-plan", name: "Plan", parent: "folder-proj-b"},
	{id: "doc-unique", name: "Unique", parent: "folder-archive"},
}

// newShuffledListingClient serves items in a nested or flat root manifest whose line order changes on every request.
func newShuffledListingClient(t *testing.T, layout string, items []listingItem, seed uint64) *cloud.Client {
	t.Helper()
	var mu sync.Mutex
	rng := rand.New(rand.NewPCG(seed, seed))
	byID := make(map[string]listingItem, len(items))
	for _, it := range items {
		byID[it.id] = it
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		var body string
		switch {
		case path == "/sync/v3/root":
			body = `{"hash":"root","generation":1,"schemaVersion":3}`
		case path == "/sync/v3/files/root":
			lines := make([]string, 0, len(items))
			for _, it := range items {
				if layout == "flat" {
					lines = append(lines, "meta-"+it.id+":"+it.id+".metadata:0:1")
				} else {
					lines = append(lines, "schema-"+it.id+":"+it.id+":0:1")
				}
			}
			mu.Lock()
			rng.Shuffle(len(lines), func(i, j int) { lines[i], lines[j] = lines[j], lines[i] })
			mu.Unlock()
			body = strings.Join(lines, "\n") + "\n"
		case strings.HasPrefix(path, "/sync/v3/files/schema-"):
			id := strings.TrimPrefix(path, "/sync/v3/files/schema-")
			body = "meta-" + id + ":" + id + ".metadata:0:1\n"
		case strings.HasPrefix(path, "/sync/v3/files/meta-"):
			it, ok := byID[strings.TrimPrefix(path, "/sync/v3/files/meta-")]
			if !ok {
				t.Errorf("unexpected metadata path: %s", path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			itemType := cloud.ItemTypeDocument
			if it.folder {
				itemType = cloud.ItemTypeCollection
			}
			b, err := json.Marshal(cloud.ItemMetadata{VisibleName: it.name, Type: itemType, Parent: it.parent, Deleted: it.deleted})
			if err != nil {
				t.Error(err)
			}
			body = string(b)
		default:
			t.Errorf("unexpected path: %s", path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	client, err := cloud.NewClient(cloud.WithStorageHost(server.URL), cloud.WithConfig(&cloud.Config{UserToken: "test"}))
	if err != nil {
		t.Fatalf("failed creating client: %v", err)
	}
	return client
}

// candidateView is the comparable part of an AmbiguousCandidate.
type candidateView struct {
	ID          string
	FolderPath  string
	Unreachable bool
}

// requireAmbiguous asserts err is an AmbiguousNameError with the given query, name, and ordered candidates.
func requireAmbiguous(t *testing.T, err error, query, name string, want []candidateView) {
	t.Helper()
	if !errors.Is(err, cloud.ErrAmbiguousName) {
		t.Fatalf("expected ErrAmbiguousName, got %v", err)
	}
	if errors.Is(err, cloud.ErrItemNotFound) {
		t.Fatalf("ambiguity must not match ErrItemNotFound: %v", err)
	}
	var ambiguous *cloud.AmbiguousNameError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("expected *cloud.AmbiguousNameError, got %T: %v", err, err)
	}
	if ambiguous.Query != query || ambiguous.Name != name {
		t.Fatalf("query/name = %q/%q, want %q/%q", ambiguous.Query, ambiguous.Name, query, name)
	}
	got := make([]candidateView, 0, len(ambiguous.Candidates))
	for _, c := range ambiguous.Candidates {
		if c.Item == nil || c.Item.Metadata.VisibleName != name {
			t.Fatalf("candidate item does not carry the matched name %q: %+v", name, c.Item)
		}
		got = append(got, candidateView{ID: c.Item.ID, FolderPath: c.FolderPath, Unreachable: c.Unreachable})
	}
	if !slices.Equal(got, want) {
		t.Fatalf("candidates =\n%+v\nwant\n%+v", got, want)
	}
	for _, c := range want {
		if !strings.Contains(err.Error(), c.ID) {
			t.Fatalf("error text %q does not name candidate %s", err.Error(), c.ID)
		}
	}
}

func TestResolveReportsAmbiguousNamesInStableOrder(t *testing.T) {
	notes := []candidateView{
		{ID: "doc-notes-root", FolderPath: ""},
		{ID: "doc-notes-archive", FolderPath: "Archive"},
		{ID: "doc-notes-proj", FolderPath: "Work/Projects"},
		{ID: "doc-notes-old", FolderPath: "", Unreachable: true},
		{ID: "doc-notes-old-sub", FolderPath: "Sub", Unreachable: true},
	}
	projectFolders := []candidateView{
		{ID: "folder-proj-a", FolderPath: "Work"},
		{ID: "folder-proj-b", FolderPath: "Work"},
	}
	projectItems := []candidateView{
		{ID: "doc-projects", FolderPath: "Work"},
		{ID: "folder-proj-a", FolderPath: "Work"},
		{ID: "folder-proj-b", FolderPath: "Work"},
	}
	dups := []candidateView{
		{ID: "doc-dup-a", FolderPath: "Archive"},
		{ID: "doc-dup-b", FolderPath: "Archive"},
	}
	tests := []struct {
		name      string
		operation string
		query     string
		match     string
		want      []candidateView
	}{
		{"name across folders", "name", "Notes", "Notes", notes},
		{"resolve propagates name ambiguity", "resolve", "Notes", "Notes", notes},
		{"intermediate path segment", "path", "Work/Projects/Plan", "Projects", projectFolders},
		{"resolve propagates path ambiguity", "resolve", "/Work/Projects/Plan/", "Projects", projectFolders},
		{"final path segment", "path", "Work/Projects", "Projects", projectItems},
		{"same folder duplicates", "path", "Archive/Dup", "Dup", dups},
		{"same folder duplicates by name", "name", "Dup", "Dup", dups},
	}
	ctx := context.Background()
	for _, layout := range []string{"nested", "flat"} {
		for _, tt := range tests {
			t.Run(layout+"/"+tt.name, func(t *testing.T) {
				for run := range uint64(20) {
					client := newShuffledListingClient(t, layout, ambiguityFixture, run)
					var err error
					switch tt.operation {
					case "name":
						_, err = client.ResolveByName(ctx, tt.query)
					case "path":
						_, err = client.ResolveByPath(ctx, tt.query)
					case "resolve":
						_, err = client.Resolve(ctx, tt.query)
					}
					requireAmbiguous(t, err, tt.query, tt.match, tt.want)
				}
			})
		}
	}
}

func TestAmbiguousCandidatesStopFolderWalkAtBrokenParents(t *testing.T) {
	items := []listingItem{
		{id: "folder-loop-a", name: "LoopA", folder: true, parent: "folder-loop-b"},
		{id: "folder-loop-b", name: "LoopB", folder: true, parent: "folder-loop-a"},
		{id: "doc-host", name: "Host"},
		{id: "doc-in-loop", name: "Entry", parent: "folder-loop-a"},
		{id: "doc-under-document", name: "Entry", parent: "doc-host"},
		{id: "doc-missing-parent", name: "Entry", parent: "folder-absent"},
		{id: "doc-root", name: "Entry"},
	}
	want := []candidateView{
		{ID: "doc-root", FolderPath: ""},
		{ID: "doc-missing-parent", FolderPath: "", Unreachable: true},
		{ID: "doc-under-document", FolderPath: "", Unreachable: true},
		{ID: "doc-in-loop", FolderPath: "LoopB/LoopA", Unreachable: true},
	}
	for run := range uint64(10) {
		client := newShuffledListingClient(t, "nested", items, run)
		_, err := client.ResolveByName(context.Background(), "Entry")
		requireAmbiguous(t, err, "Entry", "Entry", want)
	}
}

func TestResolveUniqueNamesBesideAmbiguousOnes(t *testing.T) {
	tests := []struct {
		operation string
		query     string
		wantID    string
	}{
		{"name", "Unique", "doc-unique"},
		{"name", "Plan", "doc-plan"},
		{"resolve", "Unique", "doc-unique"},
		{"path", "Archive/Unique", "doc-unique"},
		{"path", "Work", "folder-work"},
		{"resolve", "Archive/Unique", "doc-unique"},
		{"resolve", "doc-notes-proj", "doc-notes-proj"},
	}
	ctx := context.Background()
	for _, layout := range []string{"nested", "flat"} {
		for _, tt := range tests {
			t.Run(layout+"/"+tt.operation+"/"+tt.query, func(t *testing.T) {
				client := newShuffledListingClient(t, layout, ambiguityFixture, 1)
				var item *cloud.Item
				var err error
				switch tt.operation {
				case "name":
					item, err = client.ResolveByName(ctx, tt.query)
				case "path":
					item, err = client.ResolveByPath(ctx, tt.query)
				case "resolve":
					item, err = client.Resolve(ctx, tt.query)
				}
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if item.ID != tt.wantID {
					t.Fatalf("resolved %s, want %s", item.ID, tt.wantID)
				}
			})
		}
	}
}

func TestResolveIgnoresTrashedAndDeletedNameMatches(t *testing.T) {
	items := []listingItem{
		{id: "doc-live", name: "Report", parent: "folder-a"},
		{id: "folder-a", name: "A", folder: true},
		{id: "doc-trashed", name: "Report", parent: "trash"},
		{id: "doc-deleted", name: "Report", parent: "folder-a", deleted: true},
	}
	ctx := context.Background()
	for _, query := range []string{"Report", "A/Report"} {
		t.Run(query, func(t *testing.T) {
			client := newShuffledListingClient(t, "nested", items, 7)
			item, err := client.Resolve(ctx, query)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if item.ID != "doc-live" {
				t.Fatalf("resolved %s, want doc-live", item.ID)
			}
		})
	}
}

func TestAmbiguousNameErrorText(t *testing.T) {
	err := &cloud.AmbiguousNameError{
		Query: "Work/Notes",
		Name:  "Notes",
		Candidates: []cloud.AmbiguousCandidate{
			{Item: &cloud.Item{ID: "id-root"}},
			{Item: &cloud.Item{ID: "id-work"}, FolderPath: "Work"},
			{Item: &cloud.Item{ID: "id-detached"}, Unreachable: true},
			{Item: &cloud.Item{ID: "id-sub"}, FolderPath: "Sub", Unreachable: true},
		},
	}
	want := `ambiguous item name: "Notes" in "Work/Notes" matches 4 items: id-root (root), id-work ("Work"), id-detached (unreachable), id-sub (unreachable "Sub")`
	if got := err.Error(); got != want {
		t.Fatalf("Error() =\n%s\nwant\n%s", got, want)
	}
	err.Query = "Notes"
	wantName := `ambiguous item name: "Notes" matches 4 items: id-root (root), id-work ("Work"), id-detached (unreachable), id-sub (unreachable "Sub")`
	if got := err.Error(); got != wantName {
		t.Fatalf("Error() =\n%s\nwant\n%s", got, wantName)
	}
}
