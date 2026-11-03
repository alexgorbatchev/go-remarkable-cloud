package cloud_test

import (
	"net/http"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestResolvedManifestIsReusedAndIsolated(t *testing.T) {
	blobs := blobFixture{}
	meta := blobs.add("doc.metadata", `{"visibleName":"Note","type":"DocumentType"}`)
	schema := blobs.manifest("doc", false, meta)
	root := blobs.manifest("root", true, schema)
	handler := blobs.handler(t, root.Hash)
	requests := 0
	c, err := cloud.NewClient(cloud.WithStorageHost("https://cloud.test"), cloud.WithConfig(&cloud.Config{UserToken: "test"}), cloud.WithHTTPClient(&http.Client{Transport: handlerTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/v3/files/"+schema.Hash {
			requests++
		}
		handler(w, r)
	})}}))
	if err != nil {
		t.Fatal(err)
	}
	item, err := c.Resolve(t.Context(), "doc")
	if err != nil {
		t.Fatal(err)
	}
	item.Entries[0].ID = "caller edit"
	for range 2 {
		manifest, err := item.GetManifest(t.Context())
		if err != nil || manifest.Hash != schema.Hash || manifest.Find("doc.metadata") == nil {
			t.Fatalf("manifest = %+v, %v", manifest, err)
		}
		manifest.Entries[0].ID = "another caller edit"
	}
	if requests != 1 {
		t.Fatalf("document manifest fetched %d times, want once", requests)
	}
	// Changing the requested hash must not return the original snapshot.
	item.Hash = blobs.manifest("other", false, blobs.add("other.metadata", `{}`)).Hash
	manifest, err := item.GetManifest(t.Context())
	if err != nil || manifest.Find("other.metadata") == nil {
		t.Fatalf("changed hash manifest = %+v, %v", manifest, err)
	}
}
