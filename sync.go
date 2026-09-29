package cloud

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// RootState represents the current sync generation and root schema hash.
type RootState struct {
	Hash          string `json:"hash"`
	Generation    int64  `json:"generation"`
	SchemaVersion int    `json:"schemaVersion"`
}

// SchemaEntry represents a single record in a document or root schema.
// Line format: "{hash}:{id}:{subfiles}:{size}".
type SchemaEntry struct {
	Hash     string `json:"hash"`
	ID       string `json:"id"`
	Subfiles int    `json:"subfiles"`
	Size     int64  `json:"size"`
}

// Manifest represents a collection of schema entries identified by a hash.
type Manifest struct {
	Hash    string        `json:"hash"`
	Entries []SchemaEntry `json:"entries"`
}

// Find locates the first schema entry matching the given ID.
func (m *Manifest) Find(id string) *SchemaEntry {
	for i := range m.Entries {
		if m.Entries[i].ID == id {
			return &m.Entries[i]
		}
	}
	return nil
}

// FindSuffix locates the first schema entry whose ID ends with the given suffix.
func (m *Manifest) FindSuffix(suffix string) *SchemaEntry {
	for i := range m.Entries {
		if strings.HasSuffix(m.Entries[i].ID, suffix) {
			return &m.Entries[i]
		}
	}
	return nil
}

// FilterPrefix returns all schema entries whose ID starts with the given prefix.
func (m *Manifest) FilterPrefix(prefix string) []SchemaEntry {
	var result []SchemaEntry
	for i := range m.Entries {
		if strings.HasPrefix(m.Entries[i].ID, prefix) {
			result = append(result, m.Entries[i])
		}
	}
	return result
}

// ParseManifest parses line-delimited schema records.
// Each line follows the format: "{hash}:{id}:{subfiles}:{size}".
func ParseManifest(hash string, r io.Reader) (*Manifest, error) {
	manifest := &Manifest{
		Hash:    hash,
		Entries: []SchemaEntry{},
	}

	scanner := bufio.NewScanner(r)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// First line is schema version number (e.g. "3" or "4")
		if lineNum == 1 && !strings.Contains(line, ":") {
			continue
		}

		parts := strings.Split(line, ":")
		if len(parts) < 4 {
			return nil, fmt.Errorf("%w: line %d has insufficient fields (%d < 4)", ErrInvalidSchema, lineNum, len(parts))
		}

		var entryHash, id string
		var subfiles int
		var size int64
		var err error

		if len(parts) >= 5 {
			// Format: {hash}:{type}:{id}:{subfiles}:{size}
			entryHash = parts[0]
			id = parts[2]
			subfiles, err = strconv.Atoi(parts[len(parts)-2])
			if err != nil {
				return nil, fmt.Errorf("%w: line %d invalid subfiles: %v", ErrInvalidSchema, lineNum, err)
			}
			size, err = strconv.ParseInt(parts[len(parts)-1], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: line %d invalid size: %v", ErrInvalidSchema, lineNum, err)
			}
		} else {
			// 4-part legacy format: {hash}:{id}:{subfiles}:{size}
			entryHash = parts[0]
			id = parts[1]
			subfiles, err = strconv.Atoi(parts[2])
			if err != nil {
				return nil, fmt.Errorf("%w: line %d invalid subfiles: %v", ErrInvalidSchema, lineNum, err)
			}
			size, err = strconv.ParseInt(parts[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%w: line %d invalid size: %v", ErrInvalidSchema, lineNum, err)
			}
		}

		manifest.Entries = append(manifest.Entries, SchemaEntry{
			Hash:     entryHash,
			ID:       id,
			Subfiles: subfiles,
			Size:     size,
		})
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: read error: %v", ErrInvalidSchema, err)
	}

	return manifest, nil
}

// FormatSchemaLine formats a schema entry into "{hash}:{id}:{subfiles}:{size}".
func FormatSchemaLine(entry SchemaEntry) string {
	return fmt.Sprintf("%s:%s:%d:%d\n", entry.Hash, entry.ID, entry.Subfiles, entry.Size)
}

// WriteManifest writes the manifest records to a writer.
func WriteManifest(w io.Writer, manifest *Manifest) error {
	if manifest == nil {
		return fmt.Errorf("%w: nil manifest", ErrInvalidSchema)
	}
	for _, entry := range manifest.Entries {
		if _, err := io.WriteString(w, FormatSchemaLine(entry)); err != nil {
			return err
		}
	}
	return nil
}

// RedirValue represents the redirection index of a page in a document.
type RedirValue struct {
	Value int `json:"value"`
}

// DeletedValue represents deletion status of a page.
type DeletedValue struct {
	Value int `json:"value"`
}

// CPage represents a page record inside a reMarkable .content file.
type CPage struct {
	ID      string        `json:"id"`
	Redir   *RedirValue   `json:"redir,omitempty"`
	Deleted *DeletedValue `json:"deleted,omitempty"`
}

// CPages captures the pages array inside document content.
type CPages struct {
	Pages    []CPage `json:"pages,omitempty"`
	Original struct {
		Pages []CPage `json:"pages,omitempty"`
	} `json:"original,omitempty"`
}

// DocumentContent represents the parsed JSON structure of a {id}.content file.
type DocumentContent struct {
	CPages            CPages         `json:"cPages,omitempty"`
	FileType          string         `json:"fileType,omitempty"`
	FormatVersion     int            `json:"formatVersion,omitempty"`
	PageCount         int            `json:"pageCount,omitempty"`
	Pages             []string       `json:"pages,omitempty"`
	Orientation       string         `json:"orientation,omitempty"`
	CoverPageNumber   int            `json:"coverPageNumber,omitempty"`
	OriginalPageCount int            `json:"originalPageCount,omitempty"`
	Extra             map[string]any `json:"-"`
}

// UnmarshalJSON unmarshals content JSON and captures any additional fields into Extra.
func (dc *DocumentContent) UnmarshalJSON(data []byte) error {
	type Alias DocumentContent
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(dc),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	// Remove standard fields from extra map
	delete(raw, "cPages")
	delete(raw, "fileType")
	delete(raw, "formatVersion")
	delete(raw, "pageCount")
	delete(raw, "pages")
	delete(raw, "orientation")
	delete(raw, "coverPageNumber")
	delete(raw, "originalPageCount")
	dc.Extra = raw
	return nil
}
