package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

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

	root, err := c.rootManifest(ctx)
	if err != nil {
		return nil, err
	}
	items, err := c.listFromManifest(ctx, root)
	if err != nil {
		return nil, err
	}
	if !options.IncludeDeleted {
		items = liveItems(items)
	}
	return items, nil
}

func (c *Client) rootManifest(ctx context.Context) (*Manifest, error) {
	state, err := c.GetRootState(ctx)
	if err != nil {
		return nil, err
	}
	manifest, err := c.GetManifest(ctx, state.Hash, "root.docSchema")
	if err != nil {
		return nil, fmt.Errorf("get root manifest: %w", err)
	}
	return manifest, nil
}

func flatRoot(root *Manifest) bool {
	for _, entry := range root.Entries {
		if entry.ID != "." && strings.Contains(entry.ID, ".") {
			return true
		}
	}
	return false
}

func (c *Client) listFromManifest(ctx context.Context, rootManifest *Manifest) ([]*Item, error) {
	// Schema v4's aggregate record describes the manifest, not a cloud item.
	entries := make([]SchemaEntry, 0, len(rootManifest.Entries))
	for _, entry := range rootManifest.Entries {
		if entry.ID != "." {
			entries = append(entries, entry)
		}
	}

	if flatRoot(rootManifest) {
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

			item, err := c.itemFromEntry(ctx, e)
			resultsChan <- itemResult{item: item, err: err}
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
