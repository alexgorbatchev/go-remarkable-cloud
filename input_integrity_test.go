package cloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func integrityClient(t *testing.T, handler http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	opts = append([]Option{WithHTTPClient(s.Client()), WithStorageHost(s.URL), WithAuthBaseURL(s.URL), WithConfig(&Config{UserToken: "user", DeviceToken: "device"})}, opts...)
	c, err := NewClient(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestBlobIntegrityAndPermissions(t *testing.T) {
	data := []byte("private document")
	hash := contentHash(data)
	var requests atomic.Int32
	dir := filepath.Join(t.TempDir(), "cache")
	c := integrityClient(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if _, err := w.Write(data); err != nil {
			t.Error(err)
		}
	}, WithCacheDir(dir))
	path := filepath.Join(dir, "blobs", hash)
	for _, tamper := range []bool{false, false, true} {
		if tamper {
			if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		got, err := c.GetBlob(t.Context(), hash, "document.pdf")
		if err != nil || string(got) != string(data) {
			t.Fatalf("GetBlob = %q, %v", got, err)
		}
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want initial download and corrupt-cache recovery", requests.Load())
	}
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{{dir, 0700}, {filepath.Dir(path), 0700}, {path, 0600}} {
		info, err := os.Stat(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != item.mode {
			t.Errorf("%s mode = %o, want %o", item.path, info.Mode().Perm(), item.mode)
		}
	}
}

func TestBlobRejectsInvalidHashAndNetworkBytes(t *testing.T) {
	for _, hash := range []string{"../outside", "", strings.Repeat("A", 64), contentHash([]byte("expected"))} {
		t.Run(hash, func(t *testing.T) {
			var requests atomic.Int32
			dir := t.TempDir()
			c := integrityClient(t, func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if _, err := w.Write([]byte("wrong")); err != nil {
					t.Error(err)
				}
			}, WithCacheDir(dir))
			if _, err := c.GetBlob(t.Context(), hash, "document.pdf"); err == nil {
				t.Fatal("accepted invalid hash or mismatched bytes")
			}
			wantRequests := int32(0)
			if hash == contentHash([]byte("expected")) {
				wantRequests = 1
			}
			if requests.Load() != wantRequests {
				t.Errorf("requests = %d, want %d", requests.Load(), wantRequests)
			}
			if _, err := os.Stat(filepath.Join(dir, "blobs", hash)); !os.IsNotExist(err) {
				t.Errorf("invalid blob cached: %v", err)
			}
		})
	}
}

func TestExplicitConfigFailures(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials")
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "malformed":
				if err := os.WriteFile(path, []byte(strings.Repeat("x", 70*1024)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := NewClient(WithConfigFile(path)); err == nil {
				t.Fatal("ignored explicit config error")
			}
		})
	}
}

func TestRenewalRetainsTokenOnPersistenceFailure(t *testing.T) {
	c := integrityClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("renewed")); err != nil {
			t.Error(err)
		}
	})
	c.configPath = t.TempDir()
	token, err := c.RenewToken(t.Context())
	if err == nil || token != "renewed" || c.Config().UserToken != "renewed" {
		t.Fatalf("RenewToken = %q, %v; stored token = %q", token, err, c.Config().UserToken)
	}
}

func TestResponseBodyLimits(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		for _, operation := range []string{"pair", "renew", "discovery", "root", "commit"} {
			t.Run(fmt.Sprintf("%s/%d", operation, status), func(t *testing.T) {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(status)
					// Valid JSON followed by excess whitespace must also hit the size limit.
					_, _ = io.WriteString(w, "{}"+strings.Repeat(" ", 2*1024*1024))
				}))
				defer s.Close()
				var err error
				switch operation {
				case "pair":
					_, err = PairDevice(context.Background(), s.Client(), s.URL, "code", "", "")
				case "renew":
					_, err = RenewUserToken(t.Context(), s.Client(), s.URL, "device")
				case "discovery":
					_, err = DiscoverEndpoints(t.Context(), s.Client(), s.URL)
				case "root", "commit":
					c, createErr := NewClient(WithHTTPClient(s.Client()), WithStorageHost(s.URL), WithConfig(&Config{UserToken: "user"}))
					if createErr != nil {
						t.Fatal(createErr)
					}
					if operation == "commit" {
						err = c.commitRoot(t.Context(), contentHash(nil), 1)
					} else {
						_, err = c.GetRootState(t.Context())
					}
				}
				if err == nil {
					t.Fatal("accepted oversized response")
				}
				if status == http.StatusOK && !strings.Contains(err.Error(), "limit") {
					t.Fatalf("expected response size error, got %v", err)
				}
				if status != http.StatusOK {
					var statusErr *StatusError
					if !errors.As(err, &statusErr) || len(statusErr.Body) > 4096 || !strings.Contains(err.Error(), "truncated") {
						t.Fatalf("unbounded or unmarked HTTP error: type %T, length %d", err, len(err.Error()))
					}
				}
			})
		}
	}
}

func TestStatusErrorEscapesTerminalControls(t *testing.T) {
	err := newStatusError("fetch", http.StatusBadGateway, []byte("bad\x1b[2J\r\nresponse"))
	if strings.ContainsAny(err.Error(), "\x1b\r\n") || !strings.Contains(err.Error(), `bad\x1b[2J\r\nresponse`) {
		t.Fatalf("unsafe error text: %q", err.Error())
	}
}

func TestReadBoundedBodyStopsAtLimit(t *testing.T) {
	r := strings.NewReader(strings.Repeat("x", 1000))
	if _, err := readBoundedBody(r, 8); err == nil || r.Len() != 991 {
		t.Fatalf("read beyond limit: remaining=%d, err=%v", r.Len(), err)
	}
}

func TestConfigWriteFailureRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfigFile(path, &Config{UserToken: "user"}); err == nil {
		t.Fatal("expected rename failure")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary credentials retained: %v, %v", entries, err)
	}
}
