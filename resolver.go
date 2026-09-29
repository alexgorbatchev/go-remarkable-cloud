package cloud

import (
	"context"
	"encoding/json"
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

	rootManifest, err := c.GetManifest(ctx, rootState.Hash, "root")
	if err != nil {
		return nil, fmt.Errorf("get root manifest: %w", err)
	}

	// Group entries by item UUID prefix
	type itemGroup struct {
		id           string
		schemaHash   string
		metadataHash string
		entries      []SchemaEntry
	}
	groups := make(map[string]*itemGroup)

	for _, entry := range rootManifest.Entries {
		id := entry.ID
		// Extract UUID part before any extension or slash
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
			blob, err := c.GetBlob(ctx, g.metadataHash, fmt.Sprintf("%s.metadata", g.id))
			if err == nil {
				_ = json.Unmarshal(blob, &meta)
			}
		}

		// Fallbacks if metadata was missing or empty
		if meta.VisibleName == "" {
			meta.VisibleName = g.id
		}
		if meta.Type == "" {
			meta.Type = ItemTypeDocument
		}

		if meta.Deleted && !options.IncludeDeleted {
			continue
		}

		it := &Item{
			ID:       g.id,
			Hash:     g.schemaHash,
			Metadata: meta,
			Entries:  g.entries,
			client:   c,
		}
		items = append(items, it)
	}

	return items, nil
}

// ResolveByID finds an item matching the exact UUID.
func (c *Client) ResolveByID(ctx context.Context, id string) (*Item, error) {
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
		}
	}

	// 2. If path contains slash, resolve as hierarchical path
	if strings.Contains(query, "/") {
		return c.ResolveByPath(ctx, query)
	}

	// 3. Try visible name
	if it, err := c.ResolveByName(ctx, query); err == nil {
		return it, nil
	}

	// 4. Fall back to single-segment path
	return c.ResolveByPath(ctx, query)
}
