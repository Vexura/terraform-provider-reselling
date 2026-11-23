package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *[]string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/api/v1", "rsk_secret", 5*time.Second, "test")
	if err != nil {
		t.Fatal(err)
	}
	c.MinBackoff, c.MaxBackoff = time.Millisecond, 5*time.Millisecond
	var logs []string
	c.Logger = func(_ context.Context, msg string, fields map[string]any) {
		logs = append(logs, fmt.Sprint(msg, fields))
	}
	return c, &logs
}

func TestDecodesEnvelopeAndSendsBearerKey(t *testing.T) {
	c, logs := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer rsk_secret" {
			t.Errorf("missing bearer key: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/v1/products/rootserver/ssh-keys/abc" || r.URL.RawQuery != "x=1" {
			t.Errorf("unexpected url %s", r.URL)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"ssh_key":{"id":"abc"}}}`))
	})
	var out struct {
		SSHKey struct {
			ID string `json:"id"`
		} `json:"ssh_key"`
	}
	if err := c.Get(context.Background(), "/products/rootserver/ssh-keys/abc?x=1", &out); err != nil {
		t.Fatal(err)
	}
	if out.SSHKey.ID != "abc" {
		t.Fatalf("got %+v", out)
	}
	for _, line := range *logs {
		if strings.Contains(line, "rsk_secret") {
			t.Fatalf("API key leaked into the log: %s", line)
		}
	}
}

func TestRetriesRateLimitAndServerErrorsOnReads(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		}
	})
	if err := c.Get(context.Background(), "/x", &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
}

func TestDoesNotRepeatAWriteAfterAServerErrorWithoutIdempotencyKey(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	err := c.Post(context.Background(), "/x", map[string]any{}, nil)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("expected one attempt and an error, got %d attempts, err %v", calls.Load(), err)
	}
}

func TestRepeatsAWriteWithIdempotencyKeyAndKeepsTheKey(t *testing.T) {
	var calls atomic.Int32
	keys := map[string]bool{}
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		keys[r.Header.Get("Idempotency-Key")] = true
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"success":true,"data":{"server":{"id":"s1"}}}`))
	})
	if err := c.Post(context.Background(), "/x", map[string]any{}, nil, WithIdempotencyKey("k1")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || len(keys) != 1 || !keys["k1"] {
		t.Fatalf("expected 2 attempts with the same key, got %d attempts, keys %v", calls.Load(), keys)
	}
}

func TestRetriesRateLimitedWrites(t *testing.T) {
	var calls atomic.Int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	if err := c.Post(context.Background(), "/x", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected a retry after 429, got %d attempts", calls.Load())
	}
}

func TestParsesTheErrorShapes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   any
		check  func(*testing.T, *APIError)
	}{
		{"envelope", 409, map[string]any{"success": false, "error": map[string]any{"code": "server_busy", "message": "busy"}}, func(t *testing.T, e *APIError) {
			if e.Code != "server_busy" || e.Message != "busy" || !IsBusy(e) {
				t.Errorf("got %+v", e)
			}
		}},
		{"laravel validation", 422, map[string]any{"message": "invalid", "errors": map[string]any{"hostname": []string{"bad host"}}}, func(t *testing.T, e *APIError) {
			if e.Code != "validation_error" || e.Fields["hostname"][0] != "bad host" {
				t.Errorf("got %+v", e)
			}
		}},
		{"domain field", 422, map[string]any{"success": false, "error": map[string]any{"code": "subnet_overlap", "message": "overlaps", "field": "subnet"}}, func(t *testing.T, e *APIError) {
			if e.Field != "subnet" {
				t.Errorf("got %+v", e)
			}
		}},
		{"details", 422, map[string]any{"success": false, "error": map[string]any{"code": "validation_error", "message": "x", "details": map[string]any{"source": []string{"bad"}}}}, func(t *testing.T, e *APIError) {
			if e.Fields["source"][0] != "bad" {
				t.Errorf("got %+v", e)
			}
		}},
		{"not json", 404, "plain", func(t *testing.T, e *APIError) {
			if !IsNotFound(e) {
				t.Errorf("got %+v", e)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if s, ok := tc.body.(string); ok {
					_, _ = w.Write([]byte(s))
					return
				}
				_ = json.NewEncoder(w).Encode(tc.body)
			})
			err := c.Post(context.Background(), "/x", map[string]any{}, nil)
			apiErr, ok := AsAPIError(err)
			if !ok {
				t.Fatalf("expected APIError, got %v", err)
			}
			if apiErr.Status != tc.status {
				t.Fatalf("status %d", apiErr.Status)
			}
			tc.check(t, apiErr)
		})
	}
}

func TestRejectsInvalidConfiguration(t *testing.T) {
	if _, err := New("not a url", "k", 0, ""); err == nil {
		t.Error("expected an error for a relative endpoint")
	}
	if _, err := New("", "", 0, ""); err == nil {
		t.Error("expected an error without API key")
	}
	c, err := New("", "k", 0, "")
	if err != nil || c.baseURL.String() != DefaultEndpoint {
		t.Errorf("default endpoint not used: %v %v", c, err)
	}
}
