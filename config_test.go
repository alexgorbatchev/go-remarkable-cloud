package cloud_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func TestParseConfig(t *testing.T) {
	input := `
# rmapi config
devicetoken: test-device-token-123
usertoken: test-user-token-456
`
	cfg, err := cloud.ParseConfig(strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error parsing config: %v", err)
	}
	if cfg.DeviceToken != "test-device-token-123" {
		t.Errorf("expected DeviceToken 'test-device-token-123', got '%s'", cfg.DeviceToken)
	}
	if cfg.UserToken != "test-user-token-456" {
		t.Errorf("expected UserToken 'test-user-token-456', got '%s'", cfg.UserToken)
	}
}

func TestWriteConfig(t *testing.T) {
	cfg := &cloud.Config{
		DeviceToken: "dev-abc",
		UserToken:   "usr-xyz",
	}
	var buf bytes.Buffer
	if err := cloud.WriteConfig(&buf, cfg); err != nil {
		t.Fatalf("unexpected error writing config: %v", err)
	}

	parsed, err := cloud.ParseConfig(&buf)
	if err != nil {
		t.Fatalf("unexpected error re-parsing config: %v", err)
	}
	if parsed.DeviceToken != cfg.DeviceToken || parsed.UserToken != cfg.UserToken {
		t.Errorf("config roundtrip mismatch: got %+v, want %+v", parsed, cfg)
	}
}

func TestReadAndWriteConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	confPath := filepath.Join(tmpDir, ".rmapi")

	cfg := &cloud.Config{
		DeviceToken: "d-token",
		UserToken:   "u-token",
	}

	if err := cloud.WriteConfigFile(confPath, cfg); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	info, err := os.Stat(confPath)
	if err != nil {
		t.Fatalf("failed to stat config file: %v", err)
	}
	// Check restricted permissions on unix systems
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("expected mode 0600, got %o", mode)
	}

	readCfg, err := cloud.ReadConfigFile(confPath)
	if err != nil {
		t.Fatalf("failed to read config file: %v", err)
	}
	if readCfg.DeviceToken != cfg.DeviceToken || readCfg.UserToken != cfg.UserToken {
		t.Errorf("read config mismatch: got %+v, want %+v", readCfg, cfg)
	}
}

func TestResolveConfigPath(t *testing.T) {
	// Explicit override
	override := "/custom/path/.rmapi"
	resolved, err := cloud.ResolveConfigPath(override)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != override {
		t.Errorf("expected override %s, got %s", override, resolved)
	}

	// Environment variable RMAPI_CONFIG
	tmpDir := t.TempDir()
	envConf := filepath.Join(tmpDir, ".rmapi")
	_ = os.WriteFile(envConf, []byte("devicetoken: abc\n"), 0600)

	t.Setenv("RMAPI_CONFIG", tmpDir)
	resolved, err = cloud.ResolveConfigPath("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved != envConf {
		t.Errorf("expected env resolved %s, got %s", envConf, resolved)
	}
}

func TestReadConfigFileNotFound(t *testing.T) {
	_, err := cloud.ReadConfigFile("/path/that/does/not/exist/.rmapi")
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
}
