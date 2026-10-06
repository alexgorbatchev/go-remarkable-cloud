package cloud

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// DefaultAuthBaseURL is the default authentication service URL.
const DefaultAuthBaseURL = "https://webapp-prod.cloud.remarkable.engineering"

// DefaultDeviceDesc is the default device description sent during pairing.
const DefaultDeviceDesc = "desktop-macos"

// PairDeviceRequest defines the payload for pairing a new device.
type PairDeviceRequest struct {
	Code       string `json:"code"`
	DeviceDesc string `json:"deviceDesc"`
	DeviceID   string `json:"deviceID"`
	Secret     string `json:"secret"`
}

// NewDeviceID generates a standard RFC 4122 version 4 UUID.
func NewDeviceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant 10xx
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// PairDevice exchanges an 8-character pairing code for a long-lived device token.
func PairDevice(ctx context.Context, client *http.Client, authBaseURL, code, deviceDesc, deviceID string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if authBaseURL == "" {
		authBaseURL = DefaultAuthBaseURL
	}
	if deviceDesc == "" {
		deviceDesc = DefaultDeviceDesc
	}
	if deviceID == "" {
		deviceID = NewDeviceID()
	}

	reqBody, err := json.Marshal(PairDeviceRequest{
		Code:       strings.TrimSpace(code),
		DeviceDesc: deviceDesc,
		DeviceID:   deviceID,
		Secret:     "",
	})
	if err != nil {
		return "", fmt.Errorf("marshal pair request: %w", err)
	}

	url := strings.TrimRight(authBaseURL, "/") + "/token/json/2/device/new"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("create pair request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("perform pair request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read pair response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", newStatusError("pair device", resp.StatusCode, bodyBytes)
	}

	token := strings.TrimSpace(string(bodyBytes))
	// Some endpoints may return JSON-quoted string
	token = strings.Trim(token, `"`)
	if token == "" {
		return "", fmt.Errorf("empty device token received from server")
	}
	return token, nil
}

// RenewUserToken exchanges a device token for a short-lived user/session token.
func RenewUserToken(ctx context.Context, client *http.Client, authBaseURL, deviceToken string) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if authBaseURL == "" {
		authBaseURL = DefaultAuthBaseURL
	}
	deviceToken = strings.TrimSpace(deviceToken)
	if deviceToken == "" {
		return "", fmt.Errorf("%w: missing device token", ErrUnauthorized)
	}

	url := strings.TrimRight(authBaseURL, "/") + "/token/json/2/user/new"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return "", fmt.Errorf("create renew request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+deviceToken)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("perform renew request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read renew response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", newStatusError("renew user token", resp.StatusCode, bodyBytes)
	}

	token := strings.TrimSpace(string(bodyBytes))
	token = strings.Trim(token, `"`)
	if token == "" {
		return "", fmt.Errorf("empty user token received from server")
	}
	return token, nil
}
