package cloud_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func setupResolverMockServer(t *testing.T) *httptest.Server {
	t.Helper()

	blobs := blobFixtures{}
	metaWork := blobs.add(`{"visibleName": "Work", "type": "CollectionType", "parent": "", "deleted": false}`)
	metaProj := blobs.add(`{"visibleName": "Projects", "type": "CollectionType", "parent": "folder-work", "deleted": false}`)
	metaDaily := blobs.add(`{"visibleName": "2026 - Daily", "type": "DocumentType", "parent": "folder-proj", "deleted": false}`)
	metaDeleted := blobs.add(`{"visibleName": "Old Trash", "type": "DocumentType", "parent": "", "deleted": true}`)
	metaInbox := blobs.add(`{"visibleName": "Inbox Note", "type": "DocumentType", "parent": "", "deleted": false}`)
	contentDaily := blobs.add(`{"fileType": "pdf", "pageCount": 42}`)
	schemaDaily := blobs.manifest(
		metaDaily+":doc-daily.metadata:0:100",
		contentDaily+":doc-daily.content:0:300",
		unpublished("pdf-daily")+":doc-daily.pdf:0:100000",
	)
	root := blobs.manifest(
		metaWork+":folder-work.metadata:0:100",
		unpublished("schema-work")+":folder-work.docSchema:0:200",
		metaProj+":folder-proj.metadata:0:100",
		unpublished("schema-proj")+":folder-proj.docSchema:0:200",
		metaDaily+":doc-daily.metadata:0:100",
		schemaDaily+":doc-daily.docSchema:0:200",
		contentDaily+":doc-daily.content:0:300",
		metaDeleted+":doc-deleted.metadata:0:100",
		metaInbox+":doc-inbox.metadata:0:100",
	)

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/v3/root" {
			_, _ = w.Write([]byte(`{"hash": "` + root + `", "generation": 10, "schemaVersion": 3}`))
			return
		}
		if !blobs.serve(w, r) {
			t.Logf("unhandled mock path: %s (filename: %s)", r.URL.Path, r.Header.Get("rm-filename"))
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestResolver_ListItems(t *testing.T) {
	server := setupResolverMockServer(t)
	defer server.Close()

	client, err := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "mock-token"}),
	)
	if err != nil {
		t.Fatalf("failed creating client: %v", err)
	}

	ctx := context.Background()

	// Active items (deleted excluded)
	items, err := client.ListItems(ctx)
	if err != nil {
		t.Fatalf("unexpected error listing items: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("expected 4 active items, got %d", len(items))
	}

	// Including deleted
	allItems, err := client.ListItems(ctx, cloud.WithIncludeDeleted(true))
	if err != nil {
		t.Fatalf("unexpected error listing all items: %v", err)
	}
	if len(allItems) != 5 {
		t.Fatalf("expected 5 total items including deleted, got %d", len(allItems))
	}
}

func TestResolver_ResolveByID(t *testing.T) {
	server := setupResolverMockServer(t)
	defer server.Close()

	client, _ := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "mock-token"}),
	)

	ctx := context.Background()
	item, err := client.ResolveByID(ctx, "doc-daily")
	if err != nil {
		t.Fatalf("unexpected error resolving by ID: %v", err)
	}

	if item.Metadata.VisibleName != "2026 - Daily" {
		t.Errorf("expected '2026 - Daily', got '%s'", item.Metadata.VisibleName)
	}
	if !item.IsDocument() {
		t.Error("expected item to be a document")
	}

	// Non-existent ID
	_, err = client.ResolveByID(ctx, "non-existent")
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got %v", err)
	}
}

func TestResolver_ResolveByName(t *testing.T) {
	server := setupResolverMockServer(t)
	defer server.Close()

	client, _ := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "mock-token"}),
	)

	ctx := context.Background()
	item, err := client.ResolveByName(ctx, "Inbox Note")
	if err != nil {
		t.Fatalf("unexpected error resolving by name: %v", err)
	}
	if item.ID != "doc-inbox" {
		t.Errorf("expected ID 'doc-inbox', got '%s'", item.ID)
	}

	// Non-existent name
	_, err = client.ResolveByName(ctx, "Missing Note")
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got %v", err)
	}
}

func TestResolver_ResolveByPath(t *testing.T) {
	server := setupResolverMockServer(t)
	defer server.Close()

	client, _ := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "mock-token"}),
	)

	ctx := context.Background()

	// Deep path: "Work/Projects/2026 - Daily"
	item, err := client.ResolveByPath(ctx, "Work/Projects/2026 - Daily")
	if err != nil {
		t.Fatalf("unexpected error resolving deep path: %v", err)
	}
	if item.ID != "doc-daily" {
		t.Errorf("expected ID 'doc-daily', got '%s'", item.ID)
	}

	// Folder path: "Work/Projects"
	projFolder, err := client.ResolveByPath(ctx, "Work/Projects")
	if err != nil {
		t.Fatalf("unexpected error resolving folder path: %v", err)
	}
	if projFolder.ID != "folder-proj" || !projFolder.IsCollection() {
		t.Errorf("expected collection folder-proj, got %+v", projFolder)
	}

	// Root collection: "Work"
	workFolder, err := client.ResolveByPath(ctx, "Work")
	if err != nil {
		t.Fatalf("unexpected error resolving root collection: %v", err)
	}
	if workFolder.ID != "folder-work" {
		t.Errorf("expected folder-work, got %s", workFolder.ID)
	}

	// Broken path segment
	_, err = client.ResolveByPath(ctx, "Work/NonExistent/2026 - Daily")
	if !errors.Is(err, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got %v", err)
	}
}

func TestResolver_ResolveUnified(t *testing.T) {
	server := setupResolverMockServer(t)
	defer server.Close()

	client, _ := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "mock-token"}),
	)

	ctx := context.Background()

	// By ID
	byID, err := client.Resolve(ctx, "doc-daily")
	if err != nil || byID.ID != "doc-daily" {
		t.Errorf("failed resolving by ID: %v", err)
	}

	// By Path
	byPath, err := client.Resolve(ctx, "Work/Projects/2026 - Daily")
	if err != nil || byPath.ID != "doc-daily" {
		t.Errorf("failed resolving by path: %v", err)
	}

	// By Name
	byName, err := client.Resolve(ctx, "Inbox Note")
	if err != nil || byName.ID != "doc-inbox" {
		t.Errorf("failed resolving by name: %v", err)
	}
}

func TestItem_GetManifestAndContent(t *testing.T) {
	server := setupResolverMockServer(t)
	defer server.Close()

	client, _ := cloud.NewClient(
		cloud.WithStorageHost(server.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "mock-token"}),
	)

	ctx := context.Background()
	item, err := client.ResolveByID(ctx, "doc-daily")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	manifest, err := item.GetManifest(ctx)
	if err != nil {
		t.Fatalf("unexpected error getting item manifest: %v", err)
	}
	if len(manifest.Entries) != 3 {
		t.Errorf("expected 3 entries in item manifest, got %d", len(manifest.Entries))
	}

	content, err := item.GetContent(ctx)
	if err != nil {
		t.Fatalf("unexpected error getting item content: %v", err)
	}
	if content.FileType != "pdf" || content.PageCount != 42 {
		t.Errorf("unexpected content values: %+v", content)
	}
}
