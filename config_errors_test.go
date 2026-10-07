package cloud_test

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("file permissions do not restrict root")
	}
}

// isolateDefaultConfig points every default config candidate path into dir.
func isolateDefaultConfig(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("RMAPI_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
}

func writeConfig(t *testing.T, path string, content string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
}

func TestNewClientReturnsConfigFileErrors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string) cloud.Option
		want  error
	}{
		{"unreadable explicit file", func(t *testing.T, dir string) cloud.Option {
			skipIfRoot(t)
			path := filepath.Join(dir, "config")
			writeConfig(t, path, "devicetoken: device\n", 0000)
			return cloud.WithConfigFile(path)
		}, fs.ErrPermission},
		{"unparseable explicit file", func(t *testing.T, dir string) cloud.Option {
			path := filepath.Join(dir, "config")
			writeConfig(t, path, "devicetoken: "+strings.Repeat("x", 70<<10)+"\n", 0600)
			return cloud.WithConfigFile(path)
		}, cloud.ErrInvalidConfig},
		{"unreadable default file", func(t *testing.T, dir string) cloud.Option {
			skipIfRoot(t)
			isolateDefaultConfig(t, dir)
			writeConfig(t, filepath.Join(dir, ".rmapi"), "devicetoken: device\n", 0000)
			return cloud.WithDefaultConfigFile()
		}, fs.ErrPermission},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := cloud.NewClient(tt.setup(t, t.TempDir()))
			if c != nil || !errors.Is(err, tt.want) {
				t.Fatalf("NewClient = %v, %v; want nil and an error matching %v", c, err, tt.want)
			}
		})
	}
}

func TestNewClientAcceptsAbsentConfigFile(t *testing.T) {
	tests := []struct {
		name string
		opts func(t *testing.T, dir string) []cloud.Option
		want cloud.Config
	}{
		{"missing explicit file keeps in-memory credentials", func(t *testing.T, dir string) []cloud.Option {
			return []cloud.Option{
				cloud.WithConfig(&cloud.Config{DeviceToken: "device"}),
				cloud.WithConfigFile(filepath.Join(dir, "missing")),
			}
		}, cloud.Config{DeviceToken: "device"}},
		{"no default candidate", func(t *testing.T, dir string) []cloud.Option {
			isolateDefaultConfig(t, dir)
			return []cloud.Option{cloud.WithDefaultConfigFile()}
		}, cloud.Config{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := cloud.NewClient(tt.opts(t, t.TempDir())...)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if got := c.Config(); got != tt.want {
				t.Errorf("Config() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// readOnlyConfig writes content to a config file inside a directory the test user cannot write, so
// the file reads but the atomic save of a renewed token fails.
func readOnlyConfig(t *testing.T, content string) string {
	t.Helper()
	skipIfRoot(t)
	dir := filepath.Join(t.TempDir(), "readonly")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config")
	writeConfig(t, path, content, 0600)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	return path
}

// renewalServer mints freshToken on renewal and answers the root state only for that token.
func renewalServer(t *testing.T, freshToken string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case pairPath:
			_, _ = w.Write([]byte("device"))
		case renewPath:
			_, _ = w.Write([]byte(freshToken))
		case rootPath:
			if r.Header.Get("Authorization") != "Bearer "+freshToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"hash":"root","generation":1}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestRenewTokenReturnsUnsavedToken(t *testing.T) {
	const fresh = "fresh-user-token"
	tests := []struct {
		name   string
		config string
		renew  func(c *cloud.Client) (string, error)
	}{
		{"RenewToken", "devicetoken: device\nusertoken: stale\n", func(c *cloud.Client) (string, error) {
			return c.RenewToken(context.Background())
		}},
		{"PairDevice", "", func(c *cloud.Client) (string, error) {
			return c.PairDevice(context.Background(), "code1234")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := readOnlyConfig(t, tt.config)
			server := renewalServer(t, fresh)
			c, err := cloud.NewClient(cloud.WithAuthBaseURL(server.URL), cloud.WithConfigFile(path))
			if err != nil {
				t.Fatal(err)
			}

			token, err := tt.renew(c)
			if token != fresh || !errors.Is(err, cloud.ErrConfigNotSaved) || !errors.Is(err, fs.ErrPermission) {
				t.Fatalf("token %q, err %v; want %q and ErrConfigNotSaved wrapping the write error", token, err, fresh)
			}
			if c.Config().UserToken != fresh {
				t.Errorf("client dropped the renewed token: %+v", c.Config())
			}
			if saved, err := os.ReadFile(path); err != nil || string(saved) != tt.config {
				t.Errorf("config file changed to %q, %v", saved, err)
			}
		})
	}
}

func TestRequestRenewalReportsUnsavedToken(t *testing.T) {
	const fresh = "fresh-user-token"
	tests := []struct {
		name    string
		config  string
		handled bool
	}{
		{"missing user token", "devicetoken: device\n", true},
		{"refused user token", "devicetoken: device\nusertoken: stale\n", true},
		{"no handler", "devicetoken: device\nusertoken: stale\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := readOnlyConfig(t, tt.config)
			server := renewalServer(t, fresh)
			var mu sync.Mutex
			var reported []error
			opts := []cloud.Option{cloud.WithAuthBaseURL(server.URL), cloud.WithStorageHost(server.URL), cloud.WithConfigFile(path)}
			if tt.handled {
				opts = append(opts, cloud.WithSaveErrorHandler(func(err error) {
					mu.Lock()
					defer mu.Unlock()
					reported = append(reported, err)
				}))
			}
			c, err := cloud.NewClient(opts...)
			if err != nil {
				t.Fatal(err)
			}

			root, err := c.GetRootState(context.Background())
			if err != nil || root.Hash != "root" {
				t.Fatalf("GetRootState = %+v, %v; want the root served for the renewed token", root, err)
			}
			if c.Config().UserToken != fresh {
				t.Errorf("client dropped the renewed token: %+v", c.Config())
			}
			mu.Lock()
			defer mu.Unlock()
			if tt.handled && (len(reported) != 1 || !errors.Is(reported[0], cloud.ErrConfigNotSaved) || !errors.Is(reported[0], fs.ErrPermission)) {
				t.Errorf("handler received %v, want one ErrConfigNotSaved wrapping the write error", reported)
			}
		})
	}
}
