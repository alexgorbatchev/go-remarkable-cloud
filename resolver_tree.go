package cloud

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// liveItems excludes deleted or trashed items and their descendants. Missing
// parents and cycles remain visible so callers can diagnose unreachable items.
func liveItems(items []*Item) []*Item {
	byID := make(map[string]*Item, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	blocked := make(map[string]bool, len(items))
	for _, item := range items {
		var path []string
		seen := make(map[string]bool)
		hidden := false
		for current := item; current != nil; current = byID[current.Metadata.Parent] {
			if known, ok := blocked[current.ID]; ok {
				hidden = known
				break
			}
			if seen[current.ID] {
				break
			}
			seen[current.ID] = true
			path = append(path, current.ID)
			if current.Metadata.Deleted || current.Metadata.Parent == "trash" {
				hidden = true
				break
			}
		}
		for _, id := range path {
			blocked[id] = hidden
		}
	}
	result := make([]*Item, 0, len(items))
	for _, item := range items {
		if !blocked[item.ID] {
			result = append(result, item)
		}
	}
	return result
}

func (t *itemTree) resolveName(name string) (*Item, error) {
	var matches []*Item
	for _, item := range t.byID {
		if item.Metadata.VisibleName == name {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w: name %s", ErrItemNotFound, name)
	case 1:
		return matches[0], nil
	default:
		return nil, t.ambiguous(name, name, matches)
	}
}

func (t *itemTree) resolvePath(path string) (*Item, error) {
	cleaned := strings.Trim(strings.TrimSpace(path), "/")
	if cleaned == "" {
		return nil, fmt.Errorf("%w: empty path", ErrItemNotFound)
	}
	segments := strings.Split(cleaned, "/")
	parent := ""
	var current *Item
	for i, segment := range segments {
		var matches []*Item
		for _, child := range t.children[parent] {
			if child.Metadata.VisibleName == segment && (i == len(segments)-1 || child.IsCollection()) {
				matches = append(matches, child)
			}
		}
		switch len(matches) {
		case 0:
			return nil, fmt.Errorf("%w: path segment '%s' in '%s'", ErrItemNotFound, segment, path)
		case 1:
		default:
			return nil, t.ambiguous(path, segment, matches)
		}
		current = matches[0]
		parent = current.ID
	}
	return current, nil
}

// itemTree indexes live items by ID and by parent so lookups walk folders the way ResolveByPath does.
type itemTree struct {
	byID     map[string]*Item
	children map[string][]*Item
}

// newItemTree indexes the default ListItems result, which already excludes deleted and trashed items.
func newItemTree(items []*Item) *itemTree {
	t := &itemTree{
		byID:     make(map[string]*Item, len(items)),
		children: make(map[string][]*Item),
	}
	for _, it := range items {
		t.byID[it.ID] = it
		t.children[it.Metadata.Parent] = append(t.children[it.Metadata.Parent], it)
	}
	return t
}

// folderPath joins the names of the live collections above it, top level first. It reports false
// when the walk stops at a parent that is not a live collection before reaching the root.
func (t *itemTree) folderPath(it *Item) (string, bool) {
	var names []string
	reachable := true
	seen := map[string]bool{it.ID: true}
	for parent := it.Metadata.Parent; parent != ""; {
		folder, ok := t.byID[parent]
		if !ok || !folder.IsCollection() || seen[parent] {
			reachable = false
			break
		}
		seen[parent] = true
		names = append(names, folder.Metadata.VisibleName)
		parent = folder.Metadata.Parent
	}
	slices.Reverse(names)
	return strings.Join(names, "/"), reachable
}

// ambiguous orders matches independently of listing order and wraps them in an AmbiguousNameError.
func (t *itemTree) ambiguous(query, name string, matches []*Item) *AmbiguousNameError {
	candidates := make([]AmbiguousCandidate, 0, len(matches))
	for _, it := range matches {
		folder, reachable := t.folderPath(it)
		candidates = append(candidates, AmbiguousCandidate{Item: it, FolderPath: folder, Unreachable: !reachable})
	}
	slices.SortFunc(candidates, func(a, b AmbiguousCandidate) int {
		if a.Unreachable != b.Unreachable {
			if a.Unreachable {
				return 1
			}
			return -1
		}
		return cmp.Or(strings.Compare(a.FolderPath, b.FolderPath), strings.Compare(a.Item.ID, b.Item.ID))
	})
	return &AmbiguousNameError{Query: query, Name: name, Candidates: candidates}
}
