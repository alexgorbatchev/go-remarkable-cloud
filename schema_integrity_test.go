package cloud

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestSchemaIntegrityVersions(t *testing.T) {
	// Wire order differs from document-ID order; v3 digests decoded child
	// hashes in ID order, not the manifest bytes or their hexadecimal text.
	entries := strings.Repeat("00", 32) + ":0:b:0:7\n" + strings.Repeat("11", 32) + ":0:a:0:9\n"
	digestBytes := append(bytes.Repeat([]byte{0x11}, 32), make([]byte, 32)...)
	v3Hash := fmt.Sprintf("%x", sha256.Sum256(digestBytes))
	for _, name := range []string{"root.docSchema", "document.docSchema"} {
		if err := verifyBlob(v3Hash, name, []byte("3\n"+entries)); err != nil {
			t.Fatalf("v3 %s: %v", name, err)
		}
		changed := strings.Replace(entries, strings.Repeat("11", 32), strings.Repeat("22", 32), 1)
		if err := verifyBlob(v3Hash, name, []byte("3\n"+changed)); err == nil {
			t.Fatalf("accepted corrupt v3 %s", name)
		}
	}
	v4 := []byte("4\n0:.:2:16\n" + entries)
	v4Hash := fmt.Sprintf("%x", sha256.Sum256(v4))
	if err := verifyBlob(v4Hash, "root.docSchema", v4); err != nil {
		t.Fatal(err)
	}
	if err := verifyBlob(v3Hash, "root.docSchema", v4); err == nil {
		t.Fatal("accepted child-hash digest for a v4 root")
	}
}
