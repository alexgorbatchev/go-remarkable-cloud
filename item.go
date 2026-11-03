package cloud

import (
	"context"
	"fmt"
	"slices"
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

	client   *Client
	manifest *Manifest
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

// GetManifest returns the resolved schema snapshot when its hash still matches,
// or fetches the schema by hash. Returned entries are owned by the caller.
func (it *Item) GetManifest(ctx context.Context) (*Manifest, error) {
	if it.client == nil {
		return nil, fmt.Errorf("no client bound to item")
	}
	if it.manifest != nil && it.manifest.Hash == it.Hash {
		return &Manifest{Hash: it.manifest.Hash, Entries: slices.Clone(it.manifest.Entries)}, nil
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
		if err != nil {
			return nil, fmt.Errorf("get manifest for content of %s: %w", it.ID, err)
		}
		contentEntry = m.FindSuffix(".content")
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
