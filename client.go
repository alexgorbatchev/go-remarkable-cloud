package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Option configures a Client instance.
type Option func(*Client)

// WithConfig sets the in-memory rmapi config.
func WithConfig(cfg *Config) Option {
	return func(c *Client) {
		if cfg != nil {
			c.cfg = *cfg
		}
	}
}

// WithConfigFile loads configuration from the given file path.
func WithConfigFile(path string) Option {
	return func(c *Client) {
		c.configPath = path
	}
}

// WithHTTPClient sets a custom http.Client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		c.httpClient = httpClient
	}
}

// WithEndpoints configures custom service endpoints.
func WithEndpoints(endpoints *Endpoints) Option {
	return func(c *Client) {
		c.endpoints = endpoints
	}
}

// WithAuthBaseURL sets the base URL for authentication calls.
func WithAuthBaseURL(authURL string) Option {
	return func(c *Client) {
		c.authBaseURL = strings.TrimRight(authURL, "/")
	}
}

// WithStorageHost sets the base URL for sync storage v3 calls.
func WithStorageHost(storageHost string) Option {
	return func(c *Client) {
		if c.endpoints == nil {
			c.endpoints = DefaultEndpoints()
		}
		c.endpoints.StorageHost = strings.TrimRight(storageHost, "/")
	}
}

// WithAutoRenew controls whether the client automatically mints a new user token on 401 or 403.
func WithAutoRenew(autoRenew bool) Option {
	return func(c *Client) {
		c.autoRenew = autoRenew
	}
}

// WithSaveOnRenew controls whether updated tokens are written back to configPath on renewal.
func WithSaveOnRenew(save bool) Option {
	return func(c *Client) {
		c.saveOnRenew = save
	}
}

// WithDefaultConfigFile loads configuration from the default rmapi candidate paths.
func WithDefaultConfigFile() Option {
	return func(c *Client) {
		c.loadDefaultConfig = true
	}
}

// WithCacheDir configures a local directory for caching content-addressed blobs by hash.
func WithCacheDir(dir string) Option {
	return func(c *Client) {
		c.cacheDir = dir
	}
}

// Client coordinates authentication, discovery, and sync v3 operations with reMarkable Cloud.
type Client struct {
	mu                sync.RWMutex
	httpClient        *http.Client
	cfg               Config
	configPath        string
	loadDefaultConfig bool
	endpoints         *Endpoints
	authBaseURL       string
	autoRenew         bool
	saveOnRenew       bool
	cacheDir          string
}

// NewClient creates a new Client configured with the provided options.
func NewClient(opts ...Option) (*Client, error) {
	c := &Client{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		endpoints:   DefaultEndpoints(),
		authBaseURL: DefaultAuthBaseURL,
		autoRenew:   true,
		saveOnRenew: true,
	}

	for _, opt := range opts {
		opt(c)
	}

	// If config path is specified or default loading requested, load from disk
	if c.configPath != "" {
		loadedCfg, err := ReadConfigFile(c.configPath)
		if err != nil {
			return nil, fmt.Errorf("load configured credentials: %w", err)
		}
		c.cfg = *loadedCfg
	} else if c.loadDefaultConfig && c.cfg.DeviceToken == "" && c.cfg.UserToken == "" {
		loadedCfg, path, err := LoadConfig("")
		if err == nil {
			c.cfg = *loadedCfg
			c.configPath = path
		} else if !errors.Is(err, ErrConfigNotFound) {
			return nil, fmt.Errorf("load default credentials: %w", err)
		}
	}

	return c, nil
}

// Config returns a copy of current configuration tokens.
func (c *Client) Config() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfg
}

// Endpoints returns the current endpoints used by the client.
func (c *Client) Endpoints() *Endpoints {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.endpoints
}

// PairDevice registers a pairing code, sets the device token, and mints an initial user token.
func (c *Client) PairDevice(ctx context.Context, code string) (string, error) {
	deviceToken, err := PairDevice(ctx, c.httpClient, c.authBaseURL, code, DefaultDeviceDesc, "")
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.cfg.DeviceToken = deviceToken
	c.mu.Unlock()

	userToken, err := c.RenewToken(ctx)
	if err != nil {
		return deviceToken, fmt.Errorf("device paired but initial user token renewal failed: %w", err)
	}
	return userToken, nil
}

// RenewToken mints a fresh user token from the configured device token and stores it.
// If persistence fails, it returns the new token with an error and retains it in memory.
func (c *Client) RenewToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	deviceToken := c.cfg.DeviceToken
	c.mu.Unlock()

	if deviceToken == "" {
		return "", fmt.Errorf("%w: missing device token", ErrUnauthorized)
	}

	userToken, err := RenewUserToken(ctx, c.httpClient, c.authBaseURL, deviceToken)
	if err != nil {
		// Only a rejected device token wraps ErrUnauthorized; transport, server, and response
		// failures keep their own classification so callers can retry them.
		return "", err
	}

	c.mu.Lock()
	c.cfg.UserToken = userToken
	configPath := c.configPath
	saveOnRenew := c.saveOnRenew
	cfgCopy := c.cfg
	c.mu.Unlock()

	if saveOnRenew && configPath != "" {
		if err := WriteConfigFile(configPath, &cfgCopy); err != nil {
			return userToken, fmt.Errorf("persist renewed token: %w", err)
		}
	}

	return userToken, nil
}

func (c *Client) getUserToken(ctx context.Context) (string, error) {
	c.mu.RLock()
	token := c.cfg.UserToken
	deviceToken := c.cfg.DeviceToken
	c.mu.RUnlock()

	if token != "" {
		return token, nil
	}
	if deviceToken == "" {
		return "", fmt.Errorf("%w: neither user token nor device token is configured", ErrUnauthorized)
	}
	return c.RenewToken(ctx)
}

func (c *Client) executeWithAuth(ctx context.Context, makeReq func(userToken string) (*http.Request, error)) (*http.Response, error) {
	token, err := c.getUserToken(ctx)
	if err != nil {
		return nil, &requestNotAppliedError{err}
	}

	req, err := makeReq(token)
	if err != nil {
		return nil, &requestNotAppliedError{err}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// Only explicit authentication rejections authorize a renewal and replay.
	if c.autoRenew && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxStatusBody+1))
		_ = resp.Body.Close()
		newToken, renewErr := c.RenewToken(ctx)
		if renewErr != nil {
			return nil, &requestNotAppliedError{fmt.Errorf("auth renewal failed after %d: %w", resp.StatusCode, renewErr)}
		}
		retryReq, err := makeReq(newToken)
		if err != nil {
			return nil, &requestNotAppliedError{err}
		}
		return c.httpClient.Do(retryReq)
	}

	return resp, nil
}

// GetRootState retrieves the root sync generation and schema hash.
func (c *Client) GetRootState(ctx context.Context) (*RootState, error) {
	url := fmt.Sprintf("%s/sync/v3/root", strings.TrimRight(c.endpoints.StorageHost, "/"))

	resp, err := c.executeWithAuth(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, readStatusError("get root state", resp)
	}

	var rootState RootState
	body, err := readBoundedBody(resp.Body, maxControlBody)
	if err != nil {
		return nil, fmt.Errorf("read root state: %w", err)
	}
	if err := json.Unmarshal(body, &rootState); err != nil {
		return nil, fmt.Errorf("decode root state: %w", err)
	}
	return &rootState, nil
}

// GetManifest downloads and parses the line-delimited schema records for a file hash.
func (c *Client) GetManifest(ctx context.Context, hash, filename string) (*Manifest, error) {
	if filename == "" || filename == "root" {
		filename = "root.docSchema"
	}
	data, err := c.GetBlob(ctx, hash, filename)
	if err != nil {
		return nil, err
	}
	return ParseManifest(hash, bytes.NewReader(data))
}

// GetBlob downloads and verifies up to 512 MiB for a lowercase SHA-256 hash and rm-filename.
// Document schemas and version-3 roots use ordered entry hashes; version-4 roots and other files use their bytes.
// Cache directories and files are created with modes 0700 and 0600, respectively.
func (c *Client) GetBlob(ctx context.Context, hash, filename string) ([]byte, error) {
	return c.getBlob(ctx, hash, filename, true)
}

// GetBlobFresh downloads bytes directly from cloud storage, bypassing the disk cache.
func (c *Client) GetBlobFresh(ctx context.Context, hash, filename string) ([]byte, error) {
	return c.getBlob(ctx, hash, filename, false)
}

func (c *Client) getBlob(ctx context.Context, hash, filename string, cache bool) ([]byte, error) {
	if !validBlobHash(hash) {
		return nil, fmt.Errorf("%w: expected a lowercase SHA-256 hash", ErrInvalidSchema)
	}
	if cache && c.cacheDir != "" {
		cachePath := filepath.Join(c.cacheDir, "blobs", hash)
		if data, err := readCachedBlob(cachePath); err == nil {
			if err := verifyBlob(hash, filename, data); err == nil {
				return data, nil
			}
			if err := os.Remove(cachePath); err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("remove corrupt cache entry: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read cached blob: %w", err)
		}
	}

	url := fmt.Sprintf("%s/sync/v3/files/%s", strings.TrimRight(c.endpoints.StorageHost, "/"), hash)

	resp, err := c.executeWithAuth(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if filename != "" {
			req.Header.Set("rm-filename", filename)
		}
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		statusErr := readStatusError(fmt.Sprintf("get blob %s (%s)", hash, filename), resp)
		if resp.StatusCode == http.StatusNotFound {
			statusErr.Err = ErrItemNotFound
		}
		return nil, statusErr
	}

	data, err := readBoundedBody(resp.Body, maxBlobBody)
	if err != nil {
		return nil, err
	}

	if err := verifyBlob(hash, filename, data); err != nil {
		return nil, err
	}
	if cache && c.cacheDir != "" {
		if err := writeCachedBlob(c.cacheDir, hash, data); err != nil {
			return nil, fmt.Errorf("cache downloaded blob: %w", err)
		}
	}

	return data, nil
}

// GetContent fetches and JSON-unmarshals a file blob into the target value.
func (c *Client) GetContent(ctx context.Context, hash, filename string, target any) error {
	data, err := c.GetBlob(ctx, hash, filename)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("unmarshal content (%s): %w", filename, err)
	}
	return nil
}

// GetDocumentContent downloads and parses a {id}.content file blob into a DocumentContent struct.
func (c *Client) GetDocumentContent(ctx context.Context, hash, filename string) (*DocumentContent, error) {
	var dc DocumentContent
	if err := c.GetContent(ctx, hash, filename, &dc); err != nil {
		return nil, err
	}
	return &dc, nil
}
