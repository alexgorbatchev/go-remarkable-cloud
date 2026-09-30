package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ItemType represents whether an item is a document or collection (folder).
type ItemType string

const (
	// ItemTypeDocument represents a document/notebook/PDF/EPUB item.
	ItemTypeDocument ItemType = "DocumentType"

	// ItemTypeCollection represents a folder/collection.
	ItemTypeCollection ItemType = "CollectionType"
)

// ItemMetadata corresponds to the JSON structure stored in {id}.metadata.
type ItemMetadata struct {
	VisibleName  string   `json:"visibleName"`
	Type         ItemType `json:"type"`
	Parent       string   `json:"parent"`
	Deleted      bool     `json:"deleted"`
	LastModified string   `json:"lastModified"`
	Version      int      `json:"version"`
}

// Item represents a document or collection in the reMarkable cloud.
type Item struct {
	ID       string        `json:"id"`
	Hash     string        `json:"hash"`
	Metadata ItemMetadata  `json:"metadata"`
	Entries  []SchemaEntry `json:"entries,omitempty"`

	client *Client
}

// Client returns the client associated with the item, if any.
func (it *Item) Client() *Client {
	return it.client
}

// BindClient associates a client with the item.
func (it *Item) BindClient(client *Client) {
	it.client = client
}

// IsDocument returns true if the item is a document.
func (it *Item) IsDocument() bool {
	return it.Metadata.Type == ItemTypeDocument
}

// IsCollection returns true if the item is a collection (folder).
func (it *Item) IsCollection() bool {
	return it.Metadata.Type == ItemTypeCollection
}

// GetManifest fetches the item's document schema manifest if a schema hash is known.
func (it *Item) GetManifest(ctx context.Context) (*Manifest, error) {
	if it.client == nil {
		return nil, fmt.Errorf("no client bound to item")
	}

	hash := it.Hash
	if hash == "" {
		// Look for .docSchema in item entries
		for _, e := range it.Entries {
			if strings.HasSuffix(e.ID, ".docSchema") {
				hash = e.Hash
				break
			}
		}
	}
	if hash == "" {
		return nil, fmt.Errorf("%w: missing schema hash for item %s", ErrItemNotFound, it.ID)
	}

	return it.client.GetManifest(ctx, hash, fmt.Sprintf("%s.docSchema", it.ID))
}

// GetContent downloads and parses the item's {id}.content file.
func (it *Item) GetContent(ctx context.Context) (*DocumentContent, error) {
	if it.client == nil {
		return nil, fmt.Errorf("no client bound to item")
	}

	contentEntry := it.FindEntrySuffix(".content")
	if contentEntry == nil {
		// Attempt manifest lookup first
		m, err := it.GetManifest(ctx)
		if err == nil {
			contentEntry = m.FindSuffix(".content")
		}
	}
	if contentEntry == nil {
		return nil, fmt.Errorf("%w: content entry not found for item %s", ErrItemNotFound, it.ID)
	}

	return it.client.GetDocumentContent(ctx, contentEntry.Hash, contentEntry.ID)
}

// FindEntrySuffix finds an entry in the item's known entries matching the suffix.
func (it *Item) FindEntrySuffix(suffix string) *SchemaEntry {
	for i := range it.Entries {
		if strings.HasSuffix(it.Entries[i].ID, suffix) {
			return &it.Entries[i]
		}
	}
	return nil
}

// ListOptions configures item listing behavior.
type ListOptions struct {
	IncludeDeleted bool
}

// ListOption configures ListOptions.
type ListOption func(*ListOptions)

// WithIncludeDeleted includes items flagged as deleted.
func WithIncludeDeleted(include bool) ListOption {
	return func(o *ListOptions) {
		o.IncludeDeleted = include
	}
}

// ListItems fetches the root schema and resolves all documents and collections.
func (c *Client) ListItems(ctx context.Context, opts ...ListOption) ([]*Item, error) {
	options := ListOptions{
		IncludeDeleted: false,
	}
	for _, opt := range opts {
		opt(&options)
	}

	rootState, err := c.GetRootState(ctx)
	if err != nil {
		return nil, fmt.Errorf("get root state: %w", err)
	}

	rootManifest, err := c.GetManifest(ctx, rootState.Hash, "root.docSchema")
	if err != nil {
		return nil, fmt.Errorf("get root manifest: %w", err)
	}

	// Schema v4's aggregate record describes the manifest, not a cloud item.
	entries := make([]SchemaEntry, 0, len(rootManifest.Entries))
	for _, entry := range rootManifest.Entries {
		if entry.ID != "." {
			entries = append(entries, entry)
		}
	}

	// Check if root manifest already lists sub-files (.metadata, .docSchema) directly
	hasSubfiles := false
	for _, e := range entries {
		if strings.Contains(e.ID, ".") {
			hasSubfiles = true
			break
		}
	}

	if hasSubfiles {
		// Group entries by item UUID prefix
		type itemGroup struct {
			id           string
			schemaHash   string
			metadataHash string
			entries      []SchemaEntry
		}
		groups := make(map[string]*itemGroup)

		for _, entry := range entries {
			id := entry.ID
			var itemID string
			if idx := strings.IndexAny(id, "./"); idx != -1 {
				itemID = id[:idx]
			} else {
				itemID = id
			}

			g, ok := groups[itemID]
			if !ok {
				g = &itemGroup{
					id:      itemID,
					entries: []SchemaEntry{},
				}
				groups[itemID] = g
			}
			g.entries = append(g.entries, entry)

			if strings.HasSuffix(id, ".metadata") {
				g.metadataHash = entry.Hash
			} else if strings.HasSuffix(id, ".docSchema") || id == itemID {
				g.schemaHash = entry.Hash
			}
		}

		var items []*Item
		for _, g := range groups {
			var meta ItemMetadata
			if g.metadataHash != "" {
				var err error
				meta, err = c.readItemMetadata(ctx, g.metadataHash, g.id+".metadata")
				if err != nil {
					return nil, err
				}
			}

			if meta.VisibleName == "" {
				meta.VisibleName = g.id
			}
			if meta.Type == "" {
				meta.Type = ItemTypeDocument
			}

			if !options.IncludeDeleted && (meta.Deleted || meta.Parent == "trash") {
				continue
			}

			schemaHash := g.schemaHash
			if schemaHash == "" && len(g.entries) > 0 {
				schemaHash = g.entries[0].Hash
			}

			item := &Item{
				ID:       g.id,
				Hash:     schemaHash,
				Metadata: meta,
				Entries:  g.entries,
				client:   c,
			}
			items = append(items, item)
		}
		return items, nil
	}

	// Real reMarkable Cloud: each root entry is an item UUID
	type itemResult struct {
		item *Item
		err  error
	}

	resultsChan := make(chan itemResult, len(entries))
	sem := make(chan struct{}, 16)

	for _, entry := range entries {
		go func(e SchemaEntry) {
			sem <- struct{}{}
			defer func() { <-sem }()

			itemManifest, err := c.GetManifest(ctx, e.Hash, e.ID+".docSchema")
			if err != nil {
				resultsChan <- itemResult{err: fmt.Errorf("get item %s manifest: %w", e.ID, err)}
				return
			}

			var meta ItemMetadata
			metaEntry := itemManifest.FindSuffix(".metadata")
			if metaEntry != nil {
				meta, err = c.readItemMetadata(ctx, metaEntry.Hash, metaEntry.ID)
				if err != nil {
					resultsChan <- itemResult{err: err}
					return
				}
			}

			if meta.VisibleName == "" {
				meta.VisibleName = e.ID
			}

			resultsChan <- itemResult{
				item: &Item{
					ID:       e.ID,
					Hash:     e.Hash,
					Metadata: meta,
					Entries:  itemManifest.Entries,
					client:   c,
				},
			}
		}(entry)
	}

	var items []*Item
	var errs []error
	for i := 0; i < len(entries); i++ {
		res := <-resultsChan
		if res.err != nil {
			errs = append(errs, res.err)
			continue
		}
		if res.item != nil {
			if !options.IncludeDeleted && (res.item.Metadata.Parent == "trash" || res.item.Metadata.Deleted) {
				continue
			}
			items = append(items, res.item)
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return items, nil
}

func (c *Client) readItemMetadata(ctx context.Context, hash, filename string) (ItemMetadata, error) {
	var meta ItemMetadata
	b, err := c.GetBlob(ctx, hash, filename)
	if err != nil {
		return meta, fmt.Errorf("get metadata %s: %w", filename, err)
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return meta, fmt.Errorf("decode metadata %s: %w", filename, err)
	}
	return meta, nil
}

// ResolveByID finds an item matching the exact UUID without listing all items.
func (c *Client) ResolveByID(ctx context.Context, id string) (*Item, error) {
	rootState, err := c.GetRootState(ctx)
	if err != nil {
		return nil, fmt.Errorf("get root state: %w", err)
	}
	rootManifest, err := c.GetManifest(ctx, rootState.Hash, "root.docSchema")
	if err != nil {
		return nil, fmt.Errorf("get root manifest: %w", err)
	}
	entry := rootManifest.Find(id)
	if entry != nil {
		itemManifest, err := c.GetManifest(ctx, entry.Hash, id+".docSchema")
		if err != nil {
			return nil, fmt.Errorf("get item %s manifest: %w", id, err)
		}
		var meta ItemMetadata
		if metaEntry := itemManifest.FindSuffix(".metadata"); metaEntry != nil {
			meta, err = c.readItemMetadata(ctx, metaEntry.Hash, metaEntry.ID)
			if err != nil {
				return nil, err
			}
		}
		if meta.VisibleName == "" {
			meta.VisibleName = id
		}
		return &Item{
			ID:       id,
			Hash:     entry.Hash,
			Metadata: meta,
			Entries:  itemManifest.Entries,
			client:   c,
		}, nil
	}

	// Flattened roots require grouping subfiles through the listing path.
	items, err := c.ListItems(ctx, WithIncludeDeleted(true))
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.ID == id {
			return it, nil
		}
	}
	return nil, fmt.Errorf("%w: id %s", ErrItemNotFound, id)
}

// ResolveByName finds the first non-deleted item matching the exact visibleName.
func (c *Client) ResolveByName(ctx context.Context, name string) (*Item, error) {
	items, err := c.ListItems(ctx)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.Metadata.VisibleName == name {
			return it, nil
		}
	}
	return nil, fmt.Errorf("%w: name %s", ErrItemNotFound, name)
}

// ResolveByPath resolves a hierarchical path (e.g. "Folder/Subfolder/Document").
func (c *Client) ResolveByPath(ctx context.Context, path string) (*Item, error) {
	cleaned := strings.Trim(strings.TrimSpace(path), "/")
	if cleaned == "" {
		return nil, fmt.Errorf("%w: empty path", ErrItemNotFound)
	}

	segments := strings.Split(cleaned, "/")
	items, err := c.ListItems(ctx)
	if err != nil {
		return nil, err
	}

	// Index items by parent ID
	byParent := make(map[string][]*Item)
	for _, it := range items {
		parent := it.Metadata.Parent
		if parent == "trash" {
			continue
		}
		byParent[parent] = append(byParent[parent], it)
	}

	currentParent := ""
	var currentItem *Item

	for idx, seg := range segments {
		isLast := idx == len(segments)-1
		children := byParent[currentParent]
		var matched *Item
		for _, ch := range children {
			if ch.Metadata.VisibleName == seg {
				if isLast || ch.IsCollection() {
					matched = ch
					break
				}
			}
		}

		if matched == nil {
			return nil, fmt.Errorf("%w: path segment '%s' in '%s'", ErrItemNotFound, seg, path)
		}

		currentItem = matched
		currentParent = matched.ID
	}

	return currentItem, nil
}

// Resolve resolves an item by UUID, hierarchical path, or visibleName.
func (c *Client) Resolve(ctx context.Context, query string) (*Item, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: empty query", ErrItemNotFound)
	}

	// 1. Try exact UUID if format resembles UUID or ID
	if !strings.Contains(query, "/") {
		if it, err := c.ResolveByID(ctx, query); err == nil {
			return it, nil
		} else if !errors.Is(err, ErrItemNotFound) {
			return nil, err
		}
	}

	// 2. If path contains slash, resolve as hierarchical path
	if strings.Contains(query, "/") {
		return c.ResolveByPath(ctx, query)
	}

	// 3. Try visible name
	if it, err := c.ResolveByName(ctx, query); err == nil {
		return it, nil
	} else if !errors.Is(err, ErrItemNotFound) {
		return nil, err
	}

	// 4. Fall back to single-segment path
	return c.ResolveByPath(ctx, query)
}
