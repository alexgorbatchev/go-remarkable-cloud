package cloud_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cloud "github.com/alexgorbatchev/go-remarkable-cloud"
)

// errorBodyLimit is the documented number of response body bytes a StatusError keeps.
const errorBodyLimit = 4096

// newStreamingServer answers every request with status and body.
func newStreamingServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)
	return server
}

func newStreamingClient(t *testing.T, url string, autoRenew bool) *cloud.Client {
	t.Helper()
	c, err := cloud.NewClient(
		cloud.WithStorageHost(url),
		cloud.WithAuthBaseURL(url),
		cloud.WithAutoRenew(autoRenew),
		cloud.WithSaveOnRenew(false),
		cloud.WithConfig(&cloud.Config{DeviceToken: "device", UserToken: "user"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// boundedCalls make one request each against a server URL and return the request's error.
var boundedCalls = map[string]func(t *testing.T, url string) error{
	"storage status": func(t *testing.T, url string) error {
		_, err := newStreamingClient(t, url, false).GetRootState(context.Background())
		return err
	},
	"refused-token check of a 400": func(t *testing.T, url string) error {
		_, err := newStreamingClient(t, url, true).GetRootState(context.Background())
		return err
	},
	"pair device": func(t *testing.T, url string) error {
		_, err := cloud.PairDevice(context.Background(), nil, url, "code", "", "")
		return err
	},
	"renew user token": func(t *testing.T, url string) error {
		_, err := cloud.RenewUserToken(context.Background(), nil, url, "device")
		return err
	},
	"discovery": func(t *testing.T, url string) error {
		_, err := cloud.DiscoverEndpoints(context.Background(), nil, url)
		return err
	},
}

func TestStatusErrorBodiesAreBounded(t *testing.T) {
	// A proxy's error page several megabytes long.
	page := strings.Repeat("<p>upstream unavailable</p>", 1<<17)
	for name, call := range boundedCalls {
		status := http.StatusBadGateway
		if name == "refused-token check of a 400" {
			status = http.StatusBadRequest
		}
		for _, tt := range []struct {
			name      string
			body      string
			truncated bool
		}{
			{"limit", page[:errorBodyLimit], false},
			{"one byte over", page[:errorBodyLimit+1], true},
			{"megabytes over", page, true},
		} {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				err := call(t, newStreamingServer(t, status, tt.body).URL)
				var statusErr *cloud.StatusError
				if !errors.As(err, &statusErr) {
					t.Fatalf("err = %v, want *StatusError", err)
				}
				if statusErr.StatusCode != status || statusErr.Body != page[:errorBodyLimit] || statusErr.Truncated != tt.truncated {
					t.Fatalf("StatusError{StatusCode: %d, len(Body): %d, Truncated: %v}, want {%d, %d, %v}",
						statusErr.StatusCode, len(statusErr.Body), statusErr.Truncated, status, errorBodyLimit, tt.truncated)
				}
				msg := err.Error()
				if got := strings.HasSuffix(msg, page[:errorBodyLimit]+" [truncated]"); got != tt.truncated {
					t.Errorf("Error() marks truncation = %v, want %v", got, tt.truncated)
				}
				if len(msg) > errorBodyLimit+200 {
					t.Errorf("Error() is %d bytes", len(msg))
				}
			})
		}
	}
}

func TestServiceResponsesAreBounded(t *testing.T) {
	// Valid answers preceded by a megabyte of whitespace that JSON and token parsing would skip.
	padding := strings.Repeat(" ", 1<<20)
	tests := []struct {
		name string
		body string
		call func(t *testing.T, url string) error
	}{
		{"pair device", padding + "device-token", boundedCalls["pair device"]},
		{"renew user token", padding + "user-token", boundedCalls["renew user token"]},
		{"discovery", padding + `{"storage_host":"https://storage.example"}`, boundedCalls["discovery"]},
		{"root state", padding + `{"hash":"root","generation":1}`, boundedCalls["storage status"]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call(t, newStreamingServer(t, http.StatusOK, tt.body).URL)
			if err == nil || !strings.Contains(err.Error(), "response exceeds 65536 bytes") {
				t.Fatalf("err = %v, want a response size error", err)
			}
		})
	}
}

func TestServiceResponseReadErrorsAreReturned(t *testing.T) {
	for _, name := range []string{"pair device", "renew user token", "discovery", "storage status"} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Promise more bytes than are sent, so the client body read fails partway.
				w.Header().Set("Content-Length", "100")
				_, _ = fmt.Fprint(w, "partial")
			}))
			t.Cleanup(server.Close)

			err := boundedCalls[name](t, server.URL)
			if !errors.Is(err, io.ErrUnexpectedEOF) || !strings.Contains(err.Error(), "read ") {
				t.Fatalf("err = %v, want the wrapped body read error", err)
			}
		})
	}
}
