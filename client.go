package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// WithAutoRenew controls whether the client automatically mints a new user token on 401.
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
		if err == nil {
			c.cfg = *loadedCfg
		}
	} else if c.loadDefaultConfig && c.cfg.DeviceToken == "" && c.cfg.UserToken == "" {
		loadedCfg, path, err := LoadConfig("")
		if err == nil {
			c.cfg = *loadedCfg
			c.configPath = path
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
func (c *Client) RenewToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	deviceToken := c.cfg.DeviceToken
	c.mu.Unlock()

	if deviceToken == "" {
		return "", fmt.Errorf("%w: missing device token", ErrUnauthorized)
	}

	userToken, err := RenewUserToken(ctx, c.httpClient, c.authBaseURL, deviceToken)
	if err != nil {
		return "", fmt.Errorf("%w: %v (run 'remarkable-sync auth <code>' to pair with a new code from https://my.remarkable.com/pair/app)", ErrUnauthorized, err)
	}

	c.mu.Lock()
	c.cfg.UserToken = userToken
	configPath := c.configPath
	saveOnRenew := c.saveOnRenew
	cfgCopy := c.cfg
	c.mu.Unlock()

	if saveOnRenew && configPath != "" {
		_ = WriteConfigFile(configPath, &cfgCopy)
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
		return nil, err
	}

	req, err := makeReq(token)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// Auto-renew on 401/403 or malformed token if enabled
	if c.autoRenew && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest) {
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || strings.Contains(string(bodyBytes), "token") {
			newToken, renewErr := c.RenewToken(ctx)
			if renewErr != nil {
				return nil, fmt.Errorf("auth renewal failed after %d: %w", resp.StatusCode, renewErr)
			}

			retryReq, err := makeReq(newToken)
			if err != nil {
				return nil, err
			}
			return c.httpClient.Do(retryReq)
		}
		resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
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

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get root state failed with status %d: %s", resp.StatusCode, string(body))
	}

	var rootState RootState
	if err := json.NewDecoder(resp.Body).Decode(&rootState); err != nil {
		return nil, fmt.Errorf("decode root state: %w", err)
	}
	return &rootState, nil
}

// GetManifest downloads and parses the line-delimited schema records for a file hash.
func (c *Client) GetManifest(ctx context.Context, hash, filename string) (*Manifest, error) {
	if filename == "" || filename == "root" {
		filename = "root.docSchema"
	}
	url := fmt.Sprintf("%s/sync/v3/files/%s", strings.TrimRight(c.endpoints.StorageHost, "/"), hash)

	resp, err := c.executeWithAuth(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("rm-filename", filename)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: file hash %s (%s)", ErrItemNotFound, hash, filename)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get manifest failed with status %d: %s", resp.StatusCode, string(body))
	}

	return ParseManifest(hash, resp.Body)
}

// GetBlob downloads the raw content bytes for a file identified by hash and rm-filename.
func (c *Client) GetBlob(ctx context.Context, hash, filename string) ([]byte, error) {
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

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: file %s (%s)", ErrItemNotFound, hash, filename)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get blob failed with status %d: %s", resp.StatusCode, string(body))
	}

	return io.ReadAll(resp.Body)
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
