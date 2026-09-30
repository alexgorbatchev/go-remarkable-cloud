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

func TestCoverageBoosters(t *testing.T) {
	ctx := context.Background()

	// 1. WithDefaultConfigFile
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	confPath := filepath.Join(tmpHome, ".rmapi")
	_ = cloud.WriteConfigFile(confPath, &cloud.Config{DeviceToken: "def-dev", UserToken: "def-usr"})

	cDef, err := cloud.NewClient(cloud.WithDefaultConfigFile())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cDef.Config().DeviceToken != "def-dev" {
		t.Errorf("expected def-dev, got %s", cDef.Config().DeviceToken)
	}

	// 2. getUserToken without any tokens configured
	cEmpty, _ := cloud.NewClient()
	_, err = cEmpty.GetRootState(ctx)
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized when no tokens set, got %v", err)
	}

	// 3. 401 on GetRootState, GetManifest, GetBlob with autoRenew=false
	server401 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server401.Close()

	c401, _ := cloud.NewClient(
		cloud.WithStorageHost(server401.URL),
		cloud.WithAutoRenew(false),
		cloud.WithConfig(&cloud.Config{UserToken: "stale"}),
	)

	_, err = c401.GetRootState(ctx)
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized on root 401, got %v", err)
	}
	_, err = c401.GetManifest(ctx, "hash", "")
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized on manifest 401, got %v", err)
	}
	_, err = c401.GetBlob(ctx, "hash", "")
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized on blob 401, got %v", err)
	}

	// 4. Invalid JSON in GetContent and GetDocumentContent
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-valid-json{"))
	}))
	defer serverBadJSON.Close()

	cBadJSON, _ := cloud.NewClient(
		cloud.WithStorageHost(serverBadJSON.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "valid"}),
	)

	var target map[string]any
	err = cBadJSON.GetContent(ctx, "h", "f", &target)
	if err == nil {
		t.Error("expected unmarshal error in GetContent, got nil")
	}

	_, err = cBadJSON.GetDocumentContent(ctx, "h", "f")
	if err == nil {
		t.Error("expected unmarshal error in GetDocumentContent, got nil")
	}

	// 5. DiscoverEndpoints invalid JSON
	serverBadDisc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("invalid-json"))
	}))
	defer serverBadDisc.Close()

	_, err = cloud.DiscoverEndpoints(ctx, serverBadDisc.Client(), serverBadDisc.URL)
	if err == nil {
		t.Error("expected error for malformed discovery JSON")
	}

	// 6. XDG_CONFIG_HOME candidate paths
	tmpXDG := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpXDG)
	xdgPaths := cloud.CandidateConfigPaths()
	foundXDG := false
	for _, p := range xdgPaths {
		if strings.HasPrefix(p, tmpXDG) {
			foundXDG = true
			break
		}
	}
	if !foundXDG {
		t.Errorf("expected XDG paths to include %s", tmpXDG)
	}

	// 7. WriteConfigFile invalid path error
	err = cloud.WriteConfigFile("/proc/sys/nonexistent/rmapi", &cloud.Config{DeviceToken: "d"})
	if err == nil {
		t.Error("expected write error on unwritable path")
	}

	// 8. Find and FindSuffix returning nil on Manifest
	m := &cloud.Manifest{}
	if m.Find("none") != nil {
		t.Error("expected nil for missing ID")
	}
	if m.FindSuffix("none") != nil {
		t.Error("expected nil for missing suffix")
	}

	// 9. DocumentContent unmarshal error
	var dc cloud.DocumentContent
	if err := dc.UnmarshalJSON([]byte("bad json")); err == nil {
		t.Error("expected error unmarshaling bad DocumentContent JSON")
	}

	// 10. PairDevice on client when initial renew fails
	serverPairRenewFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token/json/2/device/new" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("paired-tok"))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("renew exploded"))
		}
	}))
	defer serverPairRenewFail.Close()

	cPairFail, _ := cloud.NewClient(
		cloud.WithAuthBaseURL(serverPairRenewFail.URL),
		cloud.WithAutoRenew(false),
	)
	_, err = cPairFail.PairDevice(ctx, "code")
	if err == nil {
		t.Error("expected error when renew fails after pairing")
	}

	// 11. PairDevice and RenewUserToken 403 Forbidden
	server403 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server403.Close()

	_, err = cloud.PairDevice(ctx, server403.Client(), server403.URL, "code", "desc", "id")
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized on 403, got %v", err)
	}
	_, err = cloud.RenewUserToken(ctx, server403.Client(), server403.URL, "tok")
	if !errors.Is(err, cloud.ErrUnauthorized) {
		t.Errorf("expected ErrUnauthorized on 403, got %v", err)
	}

	// 12. Item GetManifest with empty Hash but .docSchema in Entries
	serverItemSchema := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("h1:doc1.pdf:0:100\n"))
	}))
	defer serverItemSchema.Close()

	cItemSchema, _ := cloud.NewClient(
		cloud.WithStorageHost(serverItemSchema.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "u"}),
	)

	itemWithSchemaEntry := &cloud.Item{
		ID: "item123",
		Entries: []cloud.SchemaEntry{
			{Hash: "schema-hash-xyz", ID: "item123.docSchema"},
		},
	}
	itemWithSchemaEntry.BindClient(cItemSchema)
	man, err := itemWithSchemaEntry.GetManifest(ctx)
	if err != nil {
		t.Fatalf("unexpected error getting manifest from entry: %v", err)
	}
	if len(man.Entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(man.Entries))
	}

	// 13. ResolveConfigPath candidate hit
	tmpCandDir := t.TempDir()
	candConf := filepath.Join(tmpCandDir, ".rmapi")
	_ = os.WriteFile(candConf, []byte("devicetoken: tok\n"), 0600)
	t.Setenv("HOME", tmpCandDir)
	t.Setenv("RMAPI_CONFIG", "")
	resPath, err := cloud.ResolveConfigPath("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resPath != candConf {
		t.Errorf("expected candidate %s, got %s", candConf, resPath)
	}

	// 14. RMAPI_CONFIG as a direct file & XDG unset
	t.Setenv("RMAPI_CONFIG", candConf)
	t.Setenv("XDG_CONFIG_HOME", "")
	cPaths := cloud.CandidateConfigPaths()
	if len(cPaths) == 0 || cPaths[0] != candConf {
		t.Errorf("expected direct file in candidates, got %+v", cPaths)
	}

	// 15. Discovery webapp_host and storage_host
	serverDiscFull := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"webapp_host": "https://custom-webapp.com",
			"storage_host": "https://custom-storage.com"
		}`))
	}))
	defer serverDiscFull.Close()

	discEndpoints, err := cloud.DiscoverEndpoints(ctx, serverDiscFull.Client(), serverDiscFull.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if discEndpoints.WebappHost != "https://custom-webapp.com" || discEndpoints.StorageHost != "https://custom-storage.com" {
		t.Errorf("unexpected endpoints: %+v", discEndpoints)
	}

	// 16. Fallback item in ListItems when no metadata exists
	serverNoMeta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/sync/v3/root":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"hash": "root-nometa", "generation": 1, "schemaVersion": 3}`))
		case "/sync/v3/files/root-nometa":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("hash-alone:uuid-no-meta:0:50\n"))
		case "/sync/v3/files/hash-alone":
			_, _ = w.Write([]byte("content:uuid-no-meta.content:0:50\n"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer serverNoMeta.Close()

	cNoMeta, _ := cloud.NewClient(
		cloud.WithStorageHost(serverNoMeta.URL),
		cloud.WithConfig(&cloud.Config{UserToken: "u"}),
	)
	noMetaItems, err := cNoMeta.ListItems(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(noMetaItems) != 1 || noMetaItems[0].Metadata.VisibleName != "uuid-no-meta" {
		t.Errorf("unexpected fallback item: %+v", noMetaItems)
	}

	// 18. ResolveConfigPath fallback when no candidate exists
	emptyDir := t.TempDir()
	t.Setenv("HOME", emptyDir)
	t.Setenv("RMAPI_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	resFallback, err := cloud.ResolveConfigPath("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resFallback != filepath.Join(emptyDir, ".rmapi") {
		t.Errorf("expected fallback ~/.rmapi, got %s", resFallback)
	}

	// 19. WriteConfigFile creates parent dirs
	nestedPath := filepath.Join(emptyDir, "a", "b", "c", ".rmapi")
	if err := cloud.WriteConfigFile(nestedPath, &cloud.Config{DeviceToken: "d", UserToken: "u"}); err != nil {
		t.Fatalf("failed writing to nested dir: %v", err)
	}

	// 20. WithStorageHost when endpoints is nil
	cNilEndpoints, _ := cloud.NewClient()
	optStorage := cloud.WithStorageHost("https://custom-host.com")
	optStorage(cNilEndpoints)
	if cNilEndpoints.Endpoints().StorageHost != "https://custom-host.com" {
		t.Errorf("expected custom storage host, got %s", cNilEndpoints.Endpoints().StorageHost)
	}
}
