package cloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (c *Client) itemFromEntry(ctx context.Context, entry SchemaEntry) (*Item, error) {
	manifest, err := c.GetManifest(ctx, entry.Hash, entry.ID+".docSchema")
	if err != nil {
		return nil, fmt.Errorf("get item %s manifest: %w", entry.ID, err)
	}
	var meta ItemMetadata
	if e := manifest.FindSuffix(".metadata"); e != nil {
		meta, err = c.readItemMetadata(ctx, e.Hash, e.ID)
		if err != nil {
			return nil, err
		}
	}
	if meta.VisibleName == "" {
		meta.VisibleName = entry.ID
	}
	return &Item{ID: entry.ID, Hash: entry.Hash, Metadata: meta, Entries: manifest.Entries, client: c}, nil
}

// ResolveByID finds an exact ID, including deleted items. Nested roots need only
// the matching document; flattened roots require grouping their subfiles.
func (c *Client) ResolveByID(ctx context.Context, id string) (*Item, error) {
	root, err := c.rootManifest(ctx)
	if err != nil {
		return nil, err
	}
	if !flatRoot(root) {
		if entry := root.Find(id); entry != nil && entry.ID != "." {
			return c.itemFromEntry(ctx, *entry)
		}
	} else {
		items, err := c.listFromManifest(ctx, root)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.ID == id {
				return item, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: id %s", ErrItemNotFound, id)
}

// ResolveByName finds an exact visible name among live items. Multiple matches
// produce an AmbiguousNameError containing every matching live item.
func (c *Client) ResolveByName(ctx context.Context, name string) (*Item, error) {
	items, err := c.ListItems(ctx)
	if err != nil {
		return nil, err
	}
	return newItemTree(items).resolveName(name)
}

// ResolveByPath walks live items from the root. Intermediate segments must be
// collections; ambiguous segments produce an AmbiguousNameError.
func (c *Client) ResolveByPath(ctx context.Context, path string) (*Item, error) {
	if strings.Trim(strings.TrimSpace(path), "/") == "" {
		return nil, fmt.Errorf("%w: empty path", ErrItemNotFound)
	}
	items, err := c.ListItems(ctx)
	if err != nil {
		return nil, err
	}
	return newItemTree(items).resolvePath(path)
}

// Resolve uses one root snapshot, trying an exact ID, then a path for queries
// containing a slash, then an exact visible name. Ambiguity is returned unchanged.
func (c *Client) Resolve(ctx context.Context, query string) (*Item, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("%w: empty query", ErrItemNotFound)
	}
	root, err := c.rootManifest(ctx)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(query, "/") && !flatRoot(root) {
		if entry := root.Find(query); entry != nil && entry.ID != "." {
			return c.itemFromEntry(ctx, *entry)
		}
	}
	items, err := c.listFromManifest(ctx, root)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item.ID == query {
			return item, nil
		}
	}
	tree := newItemTree(liveItems(items))
	if strings.Contains(query, "/") {
		item, pathErr := tree.resolvePath(query)
		if pathErr == nil || !errors.Is(pathErr, ErrItemNotFound) {
			return item, pathErr
		}
		item, nameErr := tree.resolveName(query)
		if errors.Is(nameErr, ErrItemNotFound) {
			return nil, pathErr
		}
		return item, nameErr
	}
	return tree.resolveName(query)
}
