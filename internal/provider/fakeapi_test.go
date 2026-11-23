package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

const testAPIKey = "rsk_test_secret_key"

// fakeAPI is an in-memory stand-in for the public API v1. Handlers are registered per test
// with patterns such as "GET /products/rootserver/ssh-keys/{id}".
type fakeAPI struct {
	t      *testing.T
	mu     sync.Mutex
	routes []fakeRoute
	server *httptest.Server
	seq    int
	// calls records "METHOD /path" of every request, in order.
	calls []string
	// headers records the Idempotency-Key per "METHOD /path".
	idempotency map[string]string
}

type fakeRoute struct {
	method   string
	segments []string
	handler  func(r *fakeRequest) (int, any)
}

type fakeRequest struct {
	*http.Request
	Params map[string]string
	Body   map[string]any
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{t: t, idempotency: map[string]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) URL() string { return f.server.URL + "/api/v1" }

// id returns a deterministic UUID-like id.
func (f *fakeAPI) id() string {
	f.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq)
}

func (f *fakeAPI) handle(route string, handler func(r *fakeRequest) (int, any)) {
	method, pattern, _ := strings.Cut(route, " ")
	f.routes = append(f.routes, fakeRoute{method: method, segments: strings.Split(strings.Trim(pattern, "/"), "/"), handler: handler})
}

func (f *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.Header.Get("Authorization") != "Bearer "+testAPIKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"success": false, "error": map[string]any{"code": "unauthorized", "message": "bad key"}})
		return
	}
	p := strings.TrimPrefix(r.URL.Path, "/api/v1")
	f.calls = append(f.calls, r.Method+" "+p)
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		f.idempotency[r.Method+" "+p] = key
	}
	segments := strings.Split(strings.Trim(p, "/"), "/")

	for _, route := range f.routes {
		if route.method != r.Method || len(route.segments) != len(segments) {
			continue
		}
		params := map[string]string{}
		matched := true
		for i, s := range route.segments {
			if strings.HasPrefix(s, "{") {
				params[strings.Trim(s, "{}")] = segments[i]
				continue
			}
			if s != segments[i] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		body := map[string]any{}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				f.t.Errorf("request body of %s %s is not JSON: %v", r.Method, p, err)
			}
		}
		status, data := route.handler(&fakeRequest{Request: r, Params: params, Body: body})
		if status >= 400 {
			writeJSON(w, status, data)
			return
		}
		writeJSON(w, status, map[string]any{"success": true, "data": data})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"success": false, "error": map[string]any{"code": "route_not_found", "message": r.Method + " " + p}})
}

func (f *fakeAPI) called(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == call {
			n++
		}
	}
	return n
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func notFound() (int, any) {
	return http.StatusNotFound, map[string]any{"success": false, "error": map[string]any{"code": "not_found", "message": "nicht gefunden"}}
}

func validationFailed(field, message string) (int, any) {
	return http.StatusUnprocessableEntity, map[string]any{"message": message, "errors": map[string]any{field: []string{message}}}
}

// copyMap makes a shallow copy so that handlers can hand out stored objects safely.
func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// merge copies the keys of src that are present into dst.
func merge(dst, src map[string]any, keys ...string) {
	for _, k := range keys {
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
}

// providerConfig is the provider block every test configuration starts with.
func providerConfig(f *fakeAPI) string {
	return fmt.Sprintf(`
provider "reselling" {
  endpoint = %q
  api_key  = %q
  timeout  = "10s"
}
`, f.URL(), testAPIKey)
}

var testProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"reselling": providerserver.NewProtocol6WithError(New("test")()),
}

func TestMain(m *testing.M) {
	pollInterval = 5 * time.Millisecond
	testTuning = func(c *client.Client) {
		c.MinBackoff = time.Millisecond
		c.MaxBackoff = 5 * time.Millisecond
	}
	os.Exit(m.Run())
}

func regexpMust(pattern string) *regexp.Regexp { return regexp.MustCompile(pattern) }
