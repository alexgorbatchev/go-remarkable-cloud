package cloud

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestCloudManifestEncoding(t *testing.T) {
	a := sha256.Sum256([]byte("a"))
	b := sha256.Sum256([]byte("b"))
	entries := []SchemaEntry{
		{ID: "b", Hash: hex.EncodeToString(b[:]), Type: "80000000", Size: 2},
		{ID: ".", Hash: "0", Subfiles: 2, Size: 3},
		{ID: "a", Hash: hex.EncodeToString(a[:]), Size: 1},
	}
	for _, root := range []bool{false, true} {
		data, hash, size, err := encodeCloudManifest(entries, root)
		if err != nil {
			t.Fatal(err)
		}
		if size != 3 {
			t.Fatalf("wrong total size: %d", size)
		}
		parsed, err := ParseManifest(hash, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		wantType := "80000000"
		if root {
			wantType = "0"
		}
		if parsed.Find("a").Type != "0" || parsed.Find("b").Type != wantType {
			t.Fatal("entry types changed")
		}
		if strings.Index(string(data), ":a:") > strings.Index(string(data), ":b:") {
			t.Fatal("manifest not sorted")
		}
		want := sha256.Sum256(append(bytes.Clone(a[:]), b[:]...))
		if root {
			want = sha256.Sum256(data)
			if !bytes.HasPrefix(data, []byte("4\n0:.:2:3\n")) {
				t.Fatal("incorrect aggregate")
			}
		}
		if hash != hex.EncodeToString(want[:]) {
			t.Fatalf("incorrect protocol hash: %s", hash)
		}
	}
	for _, invalid := range []SchemaEntry{{ID: "", Hash: hex.EncodeToString(a[:])}, {ID: "a\nb", Hash: hex.EncodeToString(a[:])}, {ID: "a", Hash: "bad"}, {ID: "a", Hash: hex.EncodeToString(a[:]), Size: -1}, {ID: "a", Hash: hex.EncodeToString(a[:]), Type: "0:0"}} {
		if _, _, _, err := encodeCloudManifest([]SchemaEntry{invalid}, false); err == nil {
			t.Fatalf("accepted invalid entry: %+v", invalid)
		}
	}
	if _, _, _, err := encodeCloudManifest([]SchemaEntry{entries[0], entries[0]}, true); err == nil {
		t.Fatal("accepted duplicate entry")
	}
}
