package cloud_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

type errWriter struct{}

func (e *errWriter) Write(p []byte) (n int, err error) {
	return 0, errors.New("write failure")
}

func TestCoverageBoost_Cloud(t *testing.T) {
	ctx := context.Background()

	// 1. WithStorageHost
	c, _ := cloud.NewClient(cloud.WithStorageHost("https://custom.storage.com"))
	if c.Endpoints().StorageHost != "https://custom.storage.com" {
		t.Errorf("expected custom storage host")
	}
	c2, _ := cloud.NewClient(cloud.WithEndpoints(cloud.DefaultEndpoints()), cloud.WithStorageHost("https://custom2.com"))
	if c2.Endpoints().StorageHost != "https://custom2.com" {
		t.Errorf("expected custom2 storage host")
	}
	_, _ = cloud.ParseConfig(strings.NewReader(""))
	_, _ = cloud.PairDevice(ctx, nil, "", "", "", "")
	_, _ = cloud.RenewUserToken(ctx, nil, "", "")

	// 2. ParseManifest error conditions & WriteManifest error
	badManifests := []string{
		"3\nnot-enough-parts\n",
		"3\nhash:id:notanumber:100\n",
		"3\nhash:id:0:notanumber\n",
	}
	for _, bm := range badManifests {
		_, err := cloud.ParseManifest("test-hash", strings.NewReader(bm))
		if err == nil {
			t.Errorf("expected error for manifest line %q", bm)
		}
	}
	manifest := &cloud.Manifest{
		Hash: "sample",
		Entries: []cloud.SchemaEntry{
			{Hash: "h", ID: "id", Subfiles: 0, Size: 10},
		},
	}
	if err := cloud.WriteManifest(&errWriter{}, manifest); err == nil {
		t.Error("expected write error in WriteManifest")
	}

	// 3. WriteConfigFile error
	err := cloud.WriteConfigFile("/dev/null/cannot/write", &cloud.Config{DeviceToken: "abc"})
	if err == nil {
		t.Error("expected error writing to invalid path")
	}

	// 4. LoadConfig when candidate doesn't exist
	cfg, _, err := cloud.LoadConfig("/path/to/nonexistent/file")
	if err == nil {
		t.Errorf("expected error for nonexistent file, got %v", cfg)
	}

	// 5. WriteConfigFile JSON and rmapi format
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "config.json")
	if err := cloud.WriteConfigFile(jsonPath, &cloud.Config{DeviceToken: "dev", UserToken: "usr"}); err != nil {
		t.Fatalf("writing json config: %v", err)
	}
	loadedCfg, err := cloud.ReadConfigFile(jsonPath)
	if err != nil || loadedCfg.DeviceToken != "dev" || loadedCfg.UserToken != "usr" {
		t.Fatalf("failed reading json config: %v, %+v", err, loadedCfg)
	}

	rmapiPath := filepath.Join(tmpDir, ".rmapi")
	if err := cloud.WriteConfigFile(rmapiPath, &cloud.Config{DeviceToken: "dev2", UserToken: "usr2"}); err != nil {
		t.Fatalf("writing rmapi config: %v", err)
	}
	loadedRmapi, err := cloud.ReadConfigFile(rmapiPath)
	if err != nil || loadedRmapi.DeviceToken != "dev2" || loadedRmapi.UserToken != "usr2" {
		t.Fatalf("failed reading rmapi config: %v, %+v", err, loadedRmapi)
	}

	// 6. Fast ResolveByID with mock server
	blobs := blobFixtures{}
	docHash := blobs.manifest(blobs.fileRecord("doc-target.metadata", `{"visibleName":"Target Note","type":"DocumentType"}`))
	rootHash := blobs.manifest(docHash + ":doc-target:0:100")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token/v2/user":
			_, _ = w.Write([]byte("mock-token"))
		case "/sync/v3/root":
			_, _ = w.Write([]byte(`{"hash":"` + rootHash + `","generation":1,"schemaVersion":3}`))
		default:
			if !blobs.serve(w, r) {
				w.WriteHeader(http.StatusNotFound)
			}
		}
	}))
	defer ts.Close()

	client, err := cloud.NewClient(
		cloud.WithConfig(&cloud.Config{DeviceToken: "dev", UserToken: "usr"}),
		cloud.WithEndpoints(&cloud.Endpoints{WebappHost: ts.URL, StorageHost: ts.URL}),
	)
	if err != nil {
		t.Fatal(err)
	}

	item, err := client.ResolveByID(ctx, "doc-target")
	if err != nil {
		t.Fatalf("ResolveByID target failed: %v", err)
	}
	if item.Metadata.VisibleName != "Target Note" {
		t.Errorf("expected Target Note, got %s", item.Metadata.VisibleName)
	}

	// ResolveByID missing item -> should hit fallback and return ErrItemNotFound
	_, errMissing := client.ResolveByID(ctx, "doc-nonexistent")
	if !errors.Is(errMissing, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got: %v", errMissing)
	}

	// ResolveByName not found
	_, errNotFound := client.ResolveByName(ctx, "DoesNotExistAtAll")
	if !errors.Is(errNotFound, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound, got: %v", errNotFound)
	}

	// 7. ExecuteWithAuth 401 retry failure
	tsUnauthorized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token/v2/user" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer tsUnauthorized.Close()

	clientUnauth, _ := cloud.NewClient(
		cloud.WithConfig(&cloud.Config{DeviceToken: "bad"}),
		cloud.WithEndpoints(&cloud.Endpoints{WebappHost: tsUnauthorized.URL, StorageHost: tsUnauthorized.URL}),
		cloud.WithAutoRenew(true),
	)
	_, err401 := clientUnauth.GetRootState(ctx)
	if err401 == nil {
		t.Error("expected error on 401 unauthorized")
	}

	// 8. GetContent error unmarshalling target
	tsContent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not valid json"))
	}))
	defer tsContent.Close()
	clientContent, _ := cloud.NewClient(
		cloud.WithConfig(&cloud.Config{UserToken: "usr"}),
		cloud.WithEndpoints(&cloud.Endpoints{WebappHost: tsContent.URL, StorageHost: tsContent.URL}),
	)
	var targetMap map[string]any
	errBadJSON := clientContent.GetContent(ctx, blobHash([]byte("not valid json")), "file", &targetMap)
	if errBadJSON == nil || !strings.Contains(errBadJSON.Error(), "unmarshal content (file)") {
		t.Errorf("expected unmarshal error on bad json in GetContent, got %v", errBadJSON)
	}

	// 9. RenewUserToken 500 error
	tsError := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("server error"))
	}))
	defer tsError.Close()

	_, err500 := cloud.RenewUserToken(ctx, nil, tsError.URL, "dev-token")
	if err500 == nil {
		t.Error("expected error on 500 in RenewUserToken")
	}
	_, errPair500 := cloud.PairDevice(ctx, nil, tsError.URL, "12345678", "desktop-macos", "dev-id")
	if errPair500 == nil {
		t.Error("expected error on 500 in PairDevice")
	}

	// 10. GetDocumentContent 404
	_, errDoc404 := client.GetDocumentContent(ctx, unpublished("nonexistent"), "nonexistent-file")
	if !errors.Is(errDoc404, cloud.ErrItemNotFound) {
		t.Errorf("expected ErrItemNotFound on 404 in GetDocumentContent, got %v", errDoc404)
	}
}

func TestResolveConfigPath_EnvPriority(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "rmapi.conf")
	_ = os.WriteFile(tmpFile, []byte("devicetoken: 123"), 0600)

	t.Setenv("RMAPI_CONFIG", tmpFile)
	if got, _ := cloud.ResolveConfigPath(""); got != tmpFile {
		t.Errorf("expected %s, got %s", tmpFile, got)
	}
}
