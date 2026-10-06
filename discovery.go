package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const (
	// DefaultDiscoveryURL is the service manager discovery endpoint.
	DefaultDiscoveryURL = "https://eu.tectonic.remarkable.com/discovery/v1/endpoints"

	// DefaultRawHost is the fallback tectonic API host.
	DefaultRawHost = "https://eu.tectonic.remarkable.com"

	// DefaultWebappHost is the fallback webapp API host.
	DefaultWebappHost = "https://webapp-prod.cloud.remarkable.engineering"

	// DefaultStorageHost is the internal sync storage host.
	DefaultStorageHost = "https://internal.cloud.remarkable.com"
)

// Endpoints contains resolved service hosts.
type Endpoints struct {
	RawHost     string `json:"raw_host"`
	WebappHost  string `json:"webapp_host"`
	StorageHost string `json:"storage_host"`
}

// DefaultEndpoints returns the standard fallback endpoint configuration.
func DefaultEndpoints() *Endpoints {
	return &Endpoints{
		RawHost:     DefaultRawHost,
		WebappHost:  DefaultWebappHost,
		StorageHost: DefaultStorageHost,
	}
}

// DiscoverEndpoints queries the discovery endpoint or returns fallback hosts if discovery fails.
func DiscoverEndpoints(ctx context.Context, client *http.Client, discoveryURL string) (*Endpoints, error) {
	endpoints := DefaultEndpoints()
	if client == nil {
		client = http.DefaultClient
	}
	if discoveryURL == "" {
		discoveryURL = DefaultDiscoveryURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return endpoints, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return endpoints, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		statusErr := readStatusError("discover endpoints", resp)
		statusErr.Err = ErrDiscoveryFailed
		return endpoints, statusErr
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return endpoints, err
	}

	var rawMap map[string]any
	if err := json.Unmarshal(bodyBytes, &rawMap); err != nil {
		return endpoints, err
	}

	// Extract raw_host and webapp_host if present
	if raw, ok := rawMap["raw_host"].(string); ok && strings.TrimSpace(raw) != "" {
		endpoints.RawHost = strings.TrimRight(strings.TrimSpace(raw), "/")
	} else if sm, ok := rawMap["service-manager"].(string); ok && strings.TrimSpace(sm) != "" {
		endpoints.RawHost = "https://" + strings.TrimRight(strings.TrimSpace(sm), "/")
	}

	if wa, ok := rawMap["webapp_host"].(string); ok && strings.TrimSpace(wa) != "" {
		endpoints.WebappHost = strings.TrimRight(strings.TrimSpace(wa), "/")
	}

	if st, ok := rawMap["storage_host"].(string); ok && strings.TrimSpace(st) != "" {
		endpoints.StorageHost = strings.TrimRight(strings.TrimSpace(st), "/")
	}

	return endpoints, nil
}
