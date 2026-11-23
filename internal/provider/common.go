package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

// pollInterval is the pause between two status checks while waiting for the API. Tests shorten it.
var pollInterval = 10 * time.Second

// testTuning lets tests shorten the retry backoff of the client.
var testTuning func(*client.Client)

func applyTestTuning(c *client.Client) {
	if testTuning != nil {
		testTuning(c)
	}
}

// base carries the configured API client into resources and data sources.
type base struct {
	client *client.Client
}

func (b *base) configure(providerData any, diags *diag.Diagnostics) {
	if providerData == nil {
		return
	}
	c, ok := providerData.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", providerData))
		return
	}
	b.client = c
}

func (b *base) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	b.configure(req.ProviderData, &resp.Diagnostics)
}

type dataBase struct {
	client *client.Client
}

func (b *dataBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *client.Client, got %T", req.ProviderData))
		return
	}
	b.client = c
}

// fieldPaths maps request field names of the API to attribute paths of a resource, so that a
// validation error is shown at the attribute it is about.
type fieldPaths map[string]path.Path

// addAPIError turns an API error into diagnostics. Validation errors (422 with per-field
// messages, or a domain error naming a field) become attribute errors where the field is known.
func addAPIError(diags *diag.Diagnostics, summary string, err error, fields fieldPaths) {
	apiErr, ok := client.AsAPIError(err)
	if !ok {
		diags.AddError(summary, err.Error())
		return
	}

	attached := false
	if len(apiErr.Fields) > 0 {
		keys := make([]string, 0, len(apiErr.Fields))
		for key := range apiErr.Fields {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if p, found := lookupField(fields, key); found {
				diags.AddAttributeError(p, summary, strings.Join(apiErr.Fields[key], " "))
				attached = true
			}
		}
	}
	if apiErr.Field != "" {
		if p, found := lookupField(fields, apiErr.Field); found {
			diags.AddAttributeError(p, summary, apiErr.Message)
			return
		}
	}
	if attached && len(apiErr.Fields) == countMapped(fields, apiErr.Fields) {
		return
	}
	diags.AddError(summary, apiErr.Error())
}

// lookupField finds the attribute for "name", "rules.0.port" style keys: the exact key first,
// then the key without list indexes, then its first segment.
func lookupField(fields fieldPaths, key string) (path.Path, bool) {
	if fields == nil {
		return path.Empty(), false
	}
	if p, ok := fields[key]; ok {
		return p, true
	}
	segments := strings.Split(key, ".")
	kept := segments[:0:0]
	for _, s := range segments {
		if s == "*" || isDigits(s) {
			continue
		}
		kept = append(kept, s)
	}
	if p, ok := fields[strings.Join(kept, ".")]; ok {
		return p, true
	}
	if p, ok := fields[segments[0]]; ok {
		return p, true
	}
	return path.Empty(), false
}

func countMapped(fields fieldPaths, messages map[string][]string) int {
	n := 0
	for key := range messages {
		if _, ok := lookupField(fields, key); ok {
			n++
		}
	}
	return n
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// errGone is returned by status checks when the resource disappeared while waiting.
var errGone = errors.New("resource no longer exists")

// waitFor calls check until it reports done, fails, or timeout passes.
func waitFor(ctx context.Context, timeout time.Duration, what string, check func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		done, err := check(ctx)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out after %s waiting for %s; raise the matching value in the timeouts block if the platform is just slow", timeout, what)
		case <-time.After(pollInterval):
		}
	}
}

// retryBusy repeats action while the API answers that another action is still running on the
// resource (server_busy, task_already_running, ...).
func retryBusy(ctx context.Context, timeout time.Duration, action func(context.Context) error) error {
	var last error
	err := waitFor(ctx, timeout, "the previous action to finish", func(ctx context.Context) (bool, error) {
		last = action(ctx)
		if last == nil {
			return true, nil
		}
		if client.IsBusy(last) {
			return false, nil
		}
		return false, last
	})
	if err != nil && last != nil && client.IsBusy(last) {
		return fmt.Errorf("%w (last answer: %v)", err, last)
	}
	return err
}

// task is the part of a rootserver task this provider needs.
type task struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
	Error  *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// waitForTask polls /products/rootserver/tasks/{id} until the task completed, failed or was cancelled.
func waitForTask(ctx context.Context, c *client.Client, taskID string, timeout time.Duration) error {
	if taskID == "" {
		return nil
	}
	return waitFor(ctx, timeout, "task "+taskID, func(ctx context.Context) (bool, error) {
		var t task
		if err := c.Get(ctx, "/products/rootserver/tasks/"+taskID, &t); err != nil {
			return false, err
		}
		switch t.Status {
		case "completed":
			return true, nil
		case "failed", "cancelled":
			msg := t.Status
			if t.Error != nil && t.Error.Message != "" {
				msg = fmt.Sprintf("%s: %s (%s)", t.Status, t.Error.Message, t.Error.Code)
			}
			return false, fmt.Errorf("task %s (%s) %s", taskID, t.Type, msg)
		}
		return false, nil
	})
}

// newIdempotencyKey returns a random key for one create call; retries of that call reuse it.
func newIdempotencyKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "tf-" + hex.EncodeToString(b)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// splitID splits an import id of the form "a/b".
func splitID(id string, parts int, format string) ([]string, error) {
	out := strings.Split(id, "/")
	if len(out) != parts {
		return nil, fmt.Errorf("expected an import id of the form %s, got %q", format, id)
	}
	for _, p := range out {
		if p == "" {
			return nil, fmt.Errorf("expected an import id of the form %s, got %q", format, id)
		}
	}
	return out, nil
}

// --- value conversion ---

func strOrNull(s *string) types.String {
	if s == nil {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

// strEmptyNull maps "" and nil to null.
func strEmptyNull(s *string) types.String {
	if s == nil || *s == "" {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

func intOrNull(i *int64) types.Int64 {
	if i == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*i)
}

func boolOrNull(b *bool) types.Bool {
	if b == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*b)
}

// optString returns a pointer for a known, non-null value (for JSON bodies with omitempty).
func optString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func optInt(v types.Int64) *int64 {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	i := v.ValueInt64()
	return &i
}

func optBool(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	b := v.ValueBool()
	return &b
}

func stringList(values []string) types.List {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elems)
}

func stringSet(values []string) types.Set {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.StringValue(v))
	}
	return types.SetValueMust(types.StringType, elems)
}

func listStrings(ctx context.Context, v types.List, diags *diag.Diagnostics) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

func setStrings(ctx context.Context, v types.Set, diags *diag.Diagnostics) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	sort.Strings(out)
	return out
}

// diffStrings returns what is in want but not in have, and what is in have but not in want.
func diffStrings(have, want []string) (added, removed []string) {
	haveSet := map[string]bool{}
	for _, h := range have {
		haveSet[h] = true
	}
	wantSet := map[string]bool{}
	for _, w := range want {
		wantSet[w] = true
		if !haveSet[w] {
			added = append(added, w)
		}
	}
	for _, h := range have {
		if !wantSet[h] {
			removed = append(removed, h)
		}
	}
	return added, removed
}

// keepListIfSameSet keeps the configured order of a list when the API returns the same
// elements in another order.
func keepListIfSameSet(ctx context.Context, current types.List, fromAPI []string) types.List {
	if !current.IsNull() && !current.IsUnknown() {
		var have []string
		if diags := current.ElementsAs(ctx, &have, false); !diags.HasError() {
			added, removed := diffStrings(have, fromAPI)
			if len(added) == 0 && len(removed) == 0 && len(have) == len(fromAPI) {
				return current
			}
		}
	}
	if len(fromAPI) == 0 && current.IsNull() {
		return current
	}
	return stringList(fromAPI)
}

// jsonEqual compares two JSON documents semantically.
func jsonEqual(a, b string) bool {
	var va, vb any
	if json.Unmarshal([]byte(a), &va) != nil || json.Unmarshal([]byte(b), &vb) != nil {
		return false
	}
	ea, _ := json.Marshal(va)
	eb, _ := json.Marshal(vb)
	return string(ea) == string(eb)
}

// idRef is the {"id": ...} object many API answers nest.
type idRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func refID(r *idRef) types.String {
	if r == nil || r.ID == "" {
		return types.StringNull()
	}
	return types.StringValue(r.ID)
}

// remarshal converts a decoded JSON value into a typed struct.
func remarshal(in, out any) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// mergeFields combines field maps; later maps win.
func mergeFields(maps ...fieldPaths) fieldPaths {
	out := fieldPaths{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}
