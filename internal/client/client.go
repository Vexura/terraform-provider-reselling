// Package client is a small HTTP client for the public API v1 of the reseller platform.
//
// Every answer of the API is an envelope: {"success": true, "data": ...} on success and
// {"success": false, "error": {"code": "...", "message": "..."}} on failure. Laravel's own
// validation answer ({"message": "...", "errors": {"field": ["..."]}}) is understood as well.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultEndpoint is the API host. The main domain sits behind a bot protection that
// refuses automated clients, so the provider talks to the API host directly.
const DefaultEndpoint = "https://api.resellingprovider.de/api/v1"

// Client talks to the API. It is safe for concurrent use.
type Client struct {
	baseURL    *url.URL
	apiKey     string
	httpClient *http.Client
	userAgent  string

	// MaxRetries is how often a request is repeated after 429 or a 5xx answer.
	MaxRetries int
	// MinBackoff and MaxBackoff bound the wait between two attempts.
	MinBackoff time.Duration
	MaxBackoff time.Duration
	// Logger receives one line per request (method, path, status, attempt). It never
	// receives headers or bodies, so neither the API key nor sensitive values can leak.
	Logger func(ctx context.Context, msg string, fields map[string]any)
}

// Option changes a single request.
type Option func(*http.Request)

// WithIdempotencyKey sends the Idempotency-Key header. Requests that carry one are also
// retried after a 5xx answer, because the API answers a repeated key with the same order.
func WithIdempotencyKey(key string) Option {
	return func(r *http.Request) { r.Header.Set("Idempotency-Key", key) }
}

// New builds a client. endpoint defaults to DefaultEndpoint.
func New(endpoint, apiKey string, timeout time.Duration, userAgent string) (*Client, error) {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	parsed, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid endpoint %q: expected an absolute URL such as %s", endpoint, DefaultEndpoint)
	}
	if apiKey == "" {
		return nil, errors.New("no API key configured")
	}
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	return &Client{
		baseURL:    parsed,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: timeout},
		userAgent:  userAgent,
		MaxRetries: 5,
		MinBackoff: 1 * time.Second,
		MaxBackoff: 30 * time.Second,
	}, nil
}

// Get fetches path and decodes the data member of the envelope into out.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

// Post sends body as JSON.
func (c *Client) Post(ctx context.Context, path string, body, out any, opts ...Option) error {
	return c.Do(ctx, http.MethodPost, path, body, out, opts...)
}

// Patch sends body as JSON.
func (c *Client) Patch(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, http.MethodPatch, path, body, out)
}

// Put sends body as JSON.
func (c *Client) Put(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, http.MethodPut, path, body, out)
}

// Delete sends a DELETE, optionally with a JSON body.
func (c *Client) Delete(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, http.MethodDelete, path, body, out)
}

// Do performs a request with retries and decodes the data member into out (when out is not nil).
func (c *Client) Do(ctx context.Context, method, path string, body, out any, opts ...Option) error {
	var payload []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		payload = encoded
	}

	target := c.baseURL.JoinPath(strings.TrimLeft(strings.SplitN(path, "?", 2)[0], "/"))
	if i := strings.Index(path, "?"); i >= 0 {
		target.RawQuery = path[i+1:]
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.userAgent != "" {
			req.Header.Set("User-Agent", c.userAgent)
		}
		for _, opt := range opts {
			opt(req)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// A network error on a request that may have reached the API is only repeated when that is harmless.
			if attempt < c.MaxRetries && c.mayRepeat(method, req, 0) {
				c.log(ctx, "retrying after network error", method, path, 0, attempt)
				if werr := c.wait(ctx, attempt, 0); werr != nil {
					return werr
				}
				continue
			}
			return fmt.Errorf("%s %s: %w", method, path, err)
		}

		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("%s %s: reading answer: %w", method, path, readErr)
		}
		c.log(ctx, "api request", method, path, resp.StatusCode, attempt)

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return decodeSuccess(raw, out)
		}

		apiErr := parseError(resp.StatusCode, raw)
		if attempt < c.MaxRetries && c.mayRepeat(method, req, resp.StatusCode) {
			if werr := c.wait(ctx, attempt, retryAfter(resp.Header.Get("Retry-After"))); werr != nil {
				return werr
			}
			continue
		}
		apiErr.Method, apiErr.Path = method, path
		return apiErr
	}
}

// mayRepeat says whether a failed attempt may be sent again. Reads are always safe. A write is
// only repeated when the API refused it before doing anything (429) or when it carries an
// Idempotency-Key, so that a lost answer can never create a second, billed resource.
func (c *Client) mayRepeat(method string, req *http.Request, status int) bool {
	idempotent := method == http.MethodGet || method == http.MethodHead || req.Header.Get("Idempotency-Key") != ""
	switch {
	case status == 0:
		return idempotent
	case status == http.StatusTooManyRequests:
		return true
	case status == http.StatusServiceUnavailable && method == http.MethodGet:
		return true
	case status >= 500:
		return idempotent
	}
	return false
}

func (c *Client) wait(ctx context.Context, attempt int, hinted time.Duration) error {
	delay := hinted
	if delay <= 0 {
		delay = c.MinBackoff << attempt
		if delay > c.MaxBackoff || delay <= 0 {
			delay = c.MaxBackoff
		}
		// Jitter keeps parallel resources from hammering the API in lock step.
		if delay > 0 {
			delay = delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
		}
	}
	if delay > c.MaxBackoff && c.MaxBackoff > 0 {
		delay = c.MaxBackoff
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) log(ctx context.Context, msg, method, path string, status, attempt int) {
	if c.Logger == nil {
		return
	}
	c.Logger(ctx, msg, map[string]any{"method": method, "path": path, "status": status, "attempt": attempt})
}

func retryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return time.Until(at)
	}
	return 0
}

type envelope struct {
	Success *bool           `json:"success"`
	Data    json.RawMessage `json:"data"`
}

func decodeSuccess(raw []byte, out any) error {
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decoding answer: %w", err)
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("decoding answer data: %w", err)
	}
	return nil
}
