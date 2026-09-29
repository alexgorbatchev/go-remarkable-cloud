package cloud_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestParseManifest(t *testing.T) {
	data := `
# Document manifest lines
hash1:doc-id.metadata:0:150
hash2:doc-id.content:0:450
hash3:doc-id/page-1.rm:0:10240
hash4:doc-id.pdf:0:204800
`
	manifest, err := cloud.ParseManifest("roothash123", strings.NewReader(data))
	if err != nil {
		t.Fatalf("unexpected error parsing manifest: %v", err)
	}

	if manifest.Hash != "roothash123" {
		t.Errorf("expected hash 'roothash123', got '%s'", manifest.Hash)
	}
	if len(manifest.Entries) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(manifest.Entries))
	}

	metaEntry := manifest.Find("doc-id.metadata")
	if metaEntry == nil {
		t.Fatal("expected to find doc-id.metadata")
	}
	if metaEntry.Hash != "hash1" || metaEntry.Size != 150 {
		t.Errorf("unexpected entry values: %+v", metaEntry)
	}

	pdfEntry := manifest.FindSuffix(".pdf")
	if pdfEntry == nil {
		t.Fatal("expected to find entry with suffix .pdf")
	}
	if pdfEntry.ID != "doc-id.pdf" {
		t.Errorf("expected ID 'doc-id.pdf', got '%s'", pdfEntry.ID)
	}

	pages := manifest.FilterPrefix("doc-id/")
	if len(pages) != 1 {
		t.Errorf("expected 1 page entry, got %d", len(pages))
	}
}

func TestWriteManifestRoundtrip(t *testing.T) {
	manifest := &cloud.Manifest{
		Hash: "root-hash",
		Entries: []cloud.SchemaEntry{
			{Hash: "h1", ID: "file1", Subfiles: 0, Size: 100},
			{Hash: "h2", ID: "file2", Subfiles: 2, Size: 500},
		},
	}

	var buf bytes.Buffer
	if err := cloud.WriteManifest(&buf, manifest); err != nil {
		t.Fatalf("failed to write manifest: %v", err)
	}

	parsed, err := cloud.ParseManifest("root-hash", &buf)
	if err != nil {
		t.Fatalf("failed to re-parse manifest: %v", err)
	}

	if len(parsed.Entries) != len(manifest.Entries) {
		t.Fatalf("roundtrip entry count mismatch: got %d, want %d", len(parsed.Entries), len(manifest.Entries))
	}
	for i := range manifest.Entries {
		if parsed.Entries[i] != manifest.Entries[i] {
			t.Errorf("entry %d mismatch: got %+v, want %+v", i, parsed.Entries[i], manifest.Entries[i])
		}
	}
}

func TestDocumentContent_Unmarshal(t *testing.T) {
	rawJSON := `{
		"cPages": {
			"pages": [
				{
					"id": "page-uuid-1",
					"redir": { "value": 0 }
				},
				{
					"id": "page-uuid-2",
					"redir": { "value": 1 },
					"deleted": { "value": 1 }
				}
			]
		},
		"fileType": "pdf",
		"formatVersion": 1,
		"pageCount": 2,
		"pages": ["page-uuid-1", "page-uuid-2"],
		"orientation": "portrait",
		"customKey": "customValue"
	}`

	var dc cloud.DocumentContent
	if err := json.Unmarshal([]byte(rawJSON), &dc); err != nil {
		t.Fatalf("failed unmarshaling document content: %v", err)
	}

	if dc.FileType != "pdf" {
		t.Errorf("expected fileType 'pdf', got '%s'", dc.FileType)
	}
	if dc.PageCount != 2 {
		t.Errorf("expected pageCount 2, got %d", dc.PageCount)
	}
	if len(dc.CPages.Pages) != 2 {
		t.Fatalf("expected 2 cPages, got %d", len(dc.CPages.Pages))
	}
	if dc.CPages.Pages[0].Redir == nil || dc.CPages.Pages[0].Redir.Value != 0 {
		t.Errorf("unexpected redir value on page 0: %+v", dc.CPages.Pages[0].Redir)
	}
	if dc.CPages.Pages[1].Deleted == nil || dc.CPages.Pages[1].Deleted.Value != 1 {
		t.Errorf("unexpected deleted value on page 1: %+v", dc.CPages.Pages[1].Deleted)
	}
	if dc.Extra["customKey"] != "customValue" {
		t.Errorf("expected extra field customKey='customValue', got '%v'", dc.Extra["customKey"])
	}
}
