package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// WithAutoRenew controls whether the client mints a new user token and replays the request once when
// the cloud refuses the token: a 401, a 403, or a 400 whose JSON message is "token is malformed".
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

// WithSaveErrorHandler sets the function that receives the error, matching ErrConfigNotSaved, when the
// client renews the user token on its own while serving a request and cannot write it to the config
// file. The request continues with the renewed token, which the client keeps in memory. RenewToken
// and PairDevice return that error to their caller instead. The handler runs synchronously on the
// goroutine that made the request, so concurrent requests can call it concurrently.
func WithSaveErrorHandler(handler func(err error)) Option {
	return func(c *Client) {
		c.saveErrorHandler = handler
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
	saveErrorHandler  func(err error)
}

// NewClient creates a new Client configured with the provided options. It returns an error when the
// config file named by WithConfigFile, or the one WithDefaultConfigFile finds, cannot be read or
// parsed. A WithConfigFile path that does not exist is not an error: the client starts with the
// credentials from WithConfig, if any, and saving a renewed token creates the file. Finding no
// default config file is not an error either.
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

	if err := c.loadConfigFile(); err != nil {
		return nil, err
	}
	return c, nil
}

// loadConfigFile replaces the in-memory credentials with the config file's when WithConfigFile names
// one or WithDefaultConfigFile finds one, as NewClient describes.
func (c *Client) loadConfigFile() error {
	if c.configPath != "" {
		loadedCfg, err := ReadConfigFile(c.configPath)
		if errors.Is(err, ErrConfigNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read config file: %w", err)
		}
		c.cfg = *loadedCfg
		return nil
	}
	if !c.loadDefaultConfig || c.cfg.DeviceToken != "" || c.cfg.UserToken != "" {
		return nil
	}
	loadedCfg, path, err := LoadConfig("")
	if errors.Is(err, ErrConfigNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read default config file: %w", err)
	}
	c.cfg = *loadedCfg
	c.configPath = path
	return nil
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

// PairDevice registers a pairing code, sets the device token, and mints an initial user token. When the
// renewed credentials cannot be saved, it returns the user token with RenewToken's ErrConfigNotSaved
// error, and the client keeps both tokens in memory.
func (c *Client) PairDevice(ctx context.Context, code string) (string, error) {
	deviceToken, err := PairDevice(ctx, c.httpClient, c.authBaseURL, code, DefaultDeviceDesc, "")
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	c.cfg.DeviceToken = deviceToken
	c.mu.Unlock()

	userToken, err := c.RenewToken(ctx)
	if errors.Is(err, ErrConfigNotSaved) {
		return userToken, err
	}
	if err != nil {
		return deviceToken, fmt.Errorf("device paired but initial user token renewal failed: %w", err)
	}
	return userToken, nil
}

// RenewToken mints a fresh user token from the configured device token and stores it in memory and,
// with WithSaveOnRenew and a config file path, in the config file. When the file cannot be written,
// it returns the new token together with an error that matches ErrConfigNotSaved and wraps the write
// error; the client keeps using the new token.
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
			return userToken, fmt.Errorf("%w: %w", ErrConfigNotSaved, err)
		}
	}

	return userToken, nil
}

// renewForRequest renews the user token for a request the client is serving. A renewed token that
// could not be saved still serves the request, and the save error goes to the WithSaveErrorHandler
// handler.
func (c *Client) renewForRequest(ctx context.Context) (string, error) {
	token, err := c.RenewToken(ctx)
	if errors.Is(err, ErrConfigNotSaved) {
		if c.saveErrorHandler != nil {
			c.saveErrorHandler(err)
		}
		return token, nil
	}
	return token, err
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
	return c.renewForRequest(ctx)
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

	if !c.autoRenew || !tokenRefused(resp) {
		return resp, nil
	}
	_ = resp.Body.Close()
	newToken, renewErr := c.renewForRequest(ctx)
	if renewErr != nil {
		return nil, &renewalError{status: resp.StatusCode, err: renewErr}
	}

	retryReq, err := makeReq(newToken)
	if err != nil {
		return nil, err
	}
	return c.httpClient.Do(retryReq)
}

// tokenRefused reports whether resp refused the request's user token, as credentialsRefused decides.
// Renewing the token and replaying any other rejected request could not change the answer. Only a
// 400 body is read, at most one byte past maxErrorBodySize so that readStatusError still sees an
// over-long body as truncated, and what was read stays readable for the caller.
func tokenRefused(resp *http.Response) bool {
	if resp.StatusCode != http.StatusBadRequest {
		return credentialsRefused(resp.StatusCode, nil, false)
	}
	// A failed read leaves a partial body that cannot match; the caller reports the 400 itself.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize+1))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return credentialsRefused(resp.StatusCode, body, len(body) > maxErrorBodySize)
}

// renewalError reports that the server refused a request's user token with status and that minting a
// new token failed, so the request was not replayed. The server answered the request, so it applied
// nothing.
type renewalError struct {
	status int
	err    error
}

func (e *renewalError) Error() string {
	return fmt.Sprintf("auth renewal failed after %d: %v", e.status, e.err)
}

// Unwrap returns the renewal failure so its classification reaches the caller.
func (e *renewalError) Unwrap() error {
	return e.err
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

	body, err := readServiceResponse("root state", resp.Body)
	if err != nil {
		return nil, err
	}
	var rootState RootState
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&rootState); err != nil {
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

// GetBlob downloads the raw content bytes for a file identified by hash and rm-filename.
func (c *Client) GetBlob(ctx context.Context, hash, filename string) ([]byte, error) {
	return c.getBlob(ctx, hash, filename, true)
}

// GetBlobFresh downloads bytes directly from cloud storage, bypassing the disk cache.
func (c *Client) GetBlobFresh(ctx context.Context, hash, filename string) ([]byte, error) {
	return c.getBlob(ctx, hash, filename, false)
}

// getBlob returns the blob for hash. It rejects a hash that is not a blob address before any request
// or cache access, so a manifest cannot inject a path into the request URL or the cache. With cache
// set and a cache directory configured, it serves an entry only after verifying it against hash and
// stores downloads, which downloadBlob has already verified.
func (c *Client) getBlob(ctx context.Context, hash, filename string, cache bool) ([]byte, error) {
	if err := validateBlobHash(hash); err != nil {
		return nil, err
	}
	if !cache || c.cacheDir == "" {
		return c.downloadBlob(ctx, hash, filename)
	}
	blobs, err := c.openBlobCache()
	if err != nil {
		// The cache only saves downloads; a directory that cannot be created or secured must not
		// fail the read, and nothing is written to it.
		return c.downloadBlob(ctx, hash, filename)
	}
	defer func() { _ = blobs.Close() }()
	if data, ok := readCachedBlob(blobs, hash, filename); ok {
		return data, nil
	}
	data, err := c.downloadBlob(ctx, hash, filename)
	if err != nil {
		return nil, err
	}
	// A failed write only costs a later download; the caller already has the verified bytes.
	_ = writeCachedBlob(blobs, hash, data)
	return data, nil
}

// downloadBlob fetches the blob for hash from cloud storage and returns ErrBlobHashMismatch when the
// bytes are not the blob hash addresses. There is no other copy to fall back on, and bytes that do
// not match the requested address are not the requested data.
func (c *Client) downloadBlob(ctx context.Context, hash, filename string) ([]byte, error) {
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

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if !blobMatchesHash(hash, filename, data) {
		return nil, fmt.Errorf("%w: get blob %s (%s)", ErrBlobHashMismatch, hash, filename)
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
