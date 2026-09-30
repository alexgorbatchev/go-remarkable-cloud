package cloud_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func blobHash(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestUpdateDocumentFiles(t *testing.T) {
	for _, failure := range []string{"", "stale", "upload", "verify", "postverify", "root-conflict", "root-response", "association", "foreign", "duplicate", "delimiter", "no-files", "root-read", "document-read"} {
		t.Run(failure, func(t *testing.T) {
			original := []byte("background")
			ink := []byte("native ink")
			pdfHash := blobHash(original)
			docData := []byte(fmt.Sprintf("3\n%s:0:doc.pdf:0:%d\n", pdfHash, len(original)))
			docHash := blobHash(docData)
			rootData := []byte(fmt.Sprintf("4\n0:.:2:20\n%s:0:doc:1:10\n%s:80000000:other:1:10\n", docHash, pdfHash))
			rootHash := blobHash(rootData)
			root := cloud.RootState{Hash: rootHash, Generation: 5, SchemaVersion: 4}
			blobs := map[string][]byte{pdfHash: original, docHash: docData, rootHash: rootData}
			var mu sync.Mutex
			puts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Header.Get("Authorization") != "Bearer test" {
					t.Error("missing auth")
				}
				if r.URL.Path == "/sync/v3/root" {
					if failure == "root-read" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					if r.Method == http.MethodPut {
						if failure == "root-conflict" {
							w.WriteHeader(http.StatusPreconditionFailed)
							return
						}
						var update struct {
							Hash       string `json:"hash"`
							Generation int64  `json:"generation"`
							Broadcast  bool   `json:"broadcast"`
						}
						if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
							t.Error(err)
						}
						if update.Generation != 5 || !update.Broadcast || r.Header.Get("rm-filename") != "roothash" {
							t.Error("invalid root update")
						}
						root.Hash = update.Hash
						root.Generation++
						if failure == "root-response" {
							fmt.Fprint(w, "invalid JSON")
							return
						}
					}
					response := root
					if failure == "association" && r.Method == http.MethodGet && root.Generation > 5 {
						response.Hash = rootHash
					}
					json.NewEncoder(w).Encode(response)
					return
				}
				hash := strings.TrimPrefix(r.URL.Path, "/sync/v3/files/")
				if failure == "document-read" && hash == docHash {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				if r.Method == http.MethodPut {
					puts++
					if failure == "upload" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if r.Header.Get("x-goog-hash") == "" || r.Header.Get("rm-filename") == "" || r.ContentLength != int64(len(data)) {
						t.Error("invalid upload headers")
					}
					blobs[hash] = data
					return
				}
				data, ok := blobs[hash]
				if !ok {
					http.NotFound(w, r)
					return
				}
				if failure == "verify" && hash == blobHash(ink) {
					data = []byte("corrupt")
				}
				if failure == "postverify" && root.Generation > 5 && hash == blobHash(ink) {
					data = []byte("corrupt after commit")
				}
				w.Write(data)
			}))
			t.Cleanup(server.Close)
			cacheDir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(cacheDir, "blobs"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cacheDir, "blobs", blobHash(ink)), ink, 0600); err != nil {
				t.Fatal(err)
			}
			client, err := cloud.NewClient(cloud.WithStorageHost(server.URL), cloud.WithConfig(&cloud.Config{UserToken: "test"}), cloud.WithCacheDir(cacheDir))
			if err != nil {
				t.Fatal(err)
			}
			expected := docHash
			if failure == "stale" {
				expected = pdfHash
			}
			files := []cloud.FileUpdate{{Name: "doc/page.rm", Data: ink}}
			switch failure {
			case "foreign":
				files[0].Name = "other/page.rm"
			case "duplicate":
				files = append(files, files[0])
			case "delimiter":
				files[0].Name = "doc/page:rm"
			case "no-files":
				files = nil
			}
			result, err := client.UpdateDocumentFiles(context.Background(), "doc", expected, files)
			if failure != "" {
				if err == nil {
					t.Fatal("expected failure")
				}
				if failure == "stale" && puts != 0 {
					t.Fatal("stale update uploaded data")
				}
				if failure == "root-response" && result.State != "commit-unknown" {
					t.Fatalf("lost uncertain commit state: %+v", result)
				}
				if failure == "association" && result.State != "committed" {
					t.Fatalf("lost committed state: %+v", result)
				}
				if failure == "postverify" && result.State != "committed" {
					t.Fatalf("lost committed state: %+v", result)
				}
				if failure == "root-conflict" && result.State != "staged" {
					t.Fatalf("conflict incorrectly reported uncertain commit: %+v", result)
				}
				if failure != "association" && failure != "root-response" && failure != "postverify" && root.Hash != rootHash {
					t.Fatal("failed staging changed root")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.State != "verified" || len(result.Uploaded) != 1 {
				t.Fatalf("incomplete result: %+v", result)
			}
			rootManifest, err := client.GetManifest(context.Background(), root.Hash, "root.docSchema")
			if err != nil {
				t.Fatal(err)
			}
			if rootManifest.Find("other") == nil || rootManifest.Find("other").Type != "0" {
				t.Fatal("other document lost or v4 type incorrect")
			}
			manifest, err := client.GetManifest(context.Background(), rootManifest.Find("doc").Hash, "doc.docSchema")
			if err != nil {
				t.Fatal(err)
			}
			if manifest.Find("doc.pdf").Hash != pdfHash || !bytes.Equal(blobs[blobHash(ink)], ink) {
				t.Fatal("background or ink changed")
			}
		})
	}
}
