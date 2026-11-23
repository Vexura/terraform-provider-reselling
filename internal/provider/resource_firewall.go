package provider

import (
	"context"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

const firewallGroupsPath = "/products/rootserver/firewall-groups"

// --- firewall group ---

var (
	_ resource.Resource                = (*firewallGroupResource)(nil)
	_ resource.ResourceWithImportState = (*firewallGroupResource)(nil)
)

type firewallGroupResource struct{ base }

// NewFirewallGroupResource manages a firewall group with its rules and attachments.
func NewFirewallGroupResource() resource.Resource { return &firewallGroupResource{} }

type firewallGroupModel struct {
	ID             types.String        `tfsdk:"id"`
	Name           types.String        `tfsdk:"name"`
	Description    types.String        `tfsdk:"description"`
	Priority       types.Int64         `tfsdk:"priority"`
	Rules          []firewallRuleModel `tfsdk:"rules"`
	ServerIDs      types.Set           `tfsdk:"server_ids"`
	LabelSelectors types.Set           `tfsdk:"label_selectors"`
}

type apiFirewallGroup struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Description    *string           `json:"description"`
	Priority       int64             `json:"priority"`
	LabelSelectors []string          `json:"label_selectors"`
	Rules          []apiFirewallRule `json:"rules"`
	Servers        []struct {
		ID                 string `json:"id"`
		AttachedExplicitly bool   `json:"attached_explicitly"`
		Removing           bool   `json:"removing"`
	} `json:"servers"`
}

var firewallGroupFields = fieldPaths{
	"name": path.Root("name"), "description": path.Root("description"), "priority": path.Root("priority"),
	"server_ids": path.Root("server_ids"), "labels": path.Root("label_selectors"), "rule_ids": path.Root("rules"),
}

func (r *firewallGroupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_firewall_group"
}

func (r *firewallGroupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A firewall group: ordered rules that apply to every attached server (`server_ids`) and to every server " +
			"carrying one of the `label_selectors`. Group rules are checked before the rules of the server itself. The rules are " +
			"written to the servers by background jobs.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Name, unique per account (max. 100 characters)."},
			"description": schema.StringAttribute{Optional: true},
			"priority": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(100),
				MarkdownDescription: "0-1000, default 100. Groups are applied in ascending priority, then by name.",
				Validators:          []validator.Int64{int64validator.Between(0, 1000)},
			},
			"rules": firewallRuleSchema(),
			"server_ids": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Servers attached explicitly. A server carries at most 5 groups.",
				Validators:          []validator.Set{setvalidator.SizeAtMost(200)},
			},
			"label_selectors": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Up to 10 labels; the group applies to every server of the account carrying one of them.",
				Validators:          []validator.Set{setvalidator.SizeAtMost(10)},
			},
		},
	}
}

func (r *firewallGroupResource) get(ctx context.Context, id string) (apiFirewallGroup, error) {
	var out struct {
		Group apiFirewallGroup `json:"group"`
	}
	err := r.client.Get(ctx, firewallGroupsPath+"/"+id, &out)
	return out.Group, err
}

func (r *firewallGroupResource) endpoints(id string) ruleEndpoints {
	base := firewallGroupsPath + "/" + id
	return ruleEndpoints{
		create:  base + "/rules/create",
		rule:    func(rid string) string { return base + "/rules/" + rid },
		reorder: base + "/rules/reorder",
	}
}

func (m *firewallGroupModel) apply(g apiFirewallGroup) {
	m.ID = types.StringValue(g.ID)
	m.Name = types.StringValue(g.Name)
	m.Description = strEmptyNull(g.Description)
	m.Priority = types.Int64Value(g.Priority)
	m.Rules = rulesForState(g.Rules, m.Rules == nil)
	explicit := []string{}
	for _, s := range g.Servers {
		if s.AttachedExplicitly && !s.Removing {
			explicit = append(explicit, s.ID)
		}
	}
	sort.Strings(explicit)
	if !(len(explicit) == 0 && m.ServerIDs.IsNull()) {
		m.ServerIDs = stringSet(explicit)
	}
	if !(len(g.LabelSelectors) == 0 && m.LabelSelectors.IsNull()) {
		m.LabelSelectors = stringSet(g.LabelSelectors)
	}
}

func (r *firewallGroupResource) sync(ctx context.Context, id string, plan firewallGroupModel, current apiFirewallGroup) (apiFirewallGroup, error) {
	reread := func(ctx context.Context) ([]apiFirewallRule, error) {
		g, err := r.get(ctx, id)
		return g.Rules, err
	}
	if err := reconcileRules(ctx, r.client, r.endpoints(id), current.Rules, plan.Rules, reread); err != nil {
		return current, err
	}

	have := []string{}
	for _, s := range current.Servers {
		if s.AttachedExplicitly && !s.Removing {
			have = append(have, s.ID)
		}
	}
	var d diag.Diagnostics
	want := setStrings(ctx, plan.ServerIDs, &d)
	added, removed := diffStrings(have, want)
	if len(added) > 0 {
		if err := r.client.Post(ctx, firewallGroupsPath+"/"+id+"/servers/attach", map[string]any{"server_ids": added}, nil); err != nil {
			return current, err
		}
	}
	if len(removed) > 0 {
		if err := r.client.Post(ctx, firewallGroupsPath+"/"+id+"/servers/detach", map[string]any{"server_ids": removed}, nil); err != nil {
			return current, err
		}
	}

	labels := setStrings(ctx, plan.LabelSelectors, &d)
	haveLabels := append([]string{}, current.LabelSelectors...)
	sort.Strings(haveLabels)
	if a, rm := diffStrings(haveLabels, labels); len(a) > 0 || len(rm) > 0 {
		if labels == nil {
			labels = []string{}
		}
		if err := r.client.Post(ctx, firewallGroupsPath+"/"+id+"/labels", map[string]any{"labels": labels}, nil); err != nil {
			return current, err
		}
	}
	return r.get(ctx, id)
}

func (r *firewallGroupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan firewallGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "priority": plan.Priority.ValueInt64()}
	if v := optString(plan.Description); v != nil {
		body["description"] = *v
	}
	var out struct {
		Group apiFirewallGroup `json:"group"`
	}
	if err := r.client.Post(ctx, firewallGroupsPath+"/create", body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create firewall group", err, firewallGroupFields)
		return
	}
	id := out.Group.ID
	state := plan
	state.ID = types.StringValue(id)
	state.Rules = nil
	state.apply(out.Group)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)

	g, err := r.sync(ctx, id, plan, out.Group)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Could not configure firewall group", err, mergeFields(firewallGroupFields, firewallRuleFields))
		return
	}
	plan.apply(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *firewallGroupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state firewallGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read firewall group", err, nil)
		return
	}
	state.apply(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *firewallGroupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state firewallGroupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if !plan.Name.Equal(state.Name) || !plan.Description.Equal(state.Description) || !plan.Priority.Equal(state.Priority) {
		body := map[string]any{"name": plan.Name.ValueString(), "description": optString(plan.Description), "priority": plan.Priority.ValueInt64()}
		if err := r.client.Post(ctx, firewallGroupsPath+"/"+id+"/update", body, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not update firewall group", err, firewallGroupFields)
			return
		}
	}
	current, err := r.get(ctx, id)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Could not read firewall group", err, nil)
		return
	}
	g, err := r.sync(ctx, id, plan, current)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Could not configure firewall group", err, mergeFields(firewallGroupFields, firewallRuleFields))
		return
	}
	plan.apply(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *firewallGroupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state firewallGroupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, firewallGroupsPath+"/"+state.ID.ValueString()+"/delete", map[string]any{"confirm": true}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete firewall group", err, nil)
	}
}

func (r *firewallGroupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// --- per-server firewall ---

var (
	_ resource.Resource                = (*rootserverFirewallResource)(nil)
	_ resource.ResourceWithImportState = (*rootserverFirewallResource)(nil)
)

type rootserverFirewallResource struct{ base }

// NewRootserverFirewallResource manages the own firewall rules of one rootserver.
func NewRootserverFirewallResource() resource.Resource { return &rootserverFirewallResource{} }

type rootserverFirewallModel struct {
	ID       types.String        `tfsdk:"id"`
	ServerID types.String        `tfsdk:"server_id"`
	Rules    []firewallRuleModel `tfsdk:"rules"`
}

func (r *rootserverFirewallResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rootserver_firewall"
}

func (r *rootserverFirewallResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The own firewall rules of a rootserver, managed as a whole: rules that are not in the configuration are " +
			"removed. Rules of firewall groups are not touched. Destroying the resource removes all own rules of the server. Import with the server id.",
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true, MarkdownDescription: "The server id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"server_id": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"rules":     firewallRuleSchema(),
		},
	}
}

func serverRuleEndpoints(serverID string) ruleEndpoints {
	base := rootserverPath + "/" + serverID + "/firewall/rules"
	return ruleEndpoints{
		create:  base + "/create",
		rule:    func(rid string) string { return base + "/" + rid },
		reorder: base + "/reorder",
	}
}

func (r *rootserverFirewallResource) rules(ctx context.Context, serverID string) ([]apiFirewallRule, error) {
	var out struct {
		Rules []apiFirewallRule `json:"rules"`
	}
	err := r.client.Get(ctx, rootserverPath+"/"+serverID+"/firewall/rules", &out)
	return out.Rules, err
}

func (r *rootserverFirewallResource) apply(ctx context.Context, plan *rootserverFirewallModel) error {
	serverID := plan.ServerID.ValueString()
	have, err := r.rules(ctx, serverID)
	if err != nil {
		return err
	}
	reread := func(ctx context.Context) ([]apiFirewallRule, error) { return r.rules(ctx, serverID) }
	if err := reconcileRules(ctx, r.client, serverRuleEndpoints(serverID), have, plan.Rules, reread); err != nil {
		return err
	}
	now, err := r.rules(ctx, serverID)
	if err != nil {
		return err
	}
	plan.ID = types.StringValue(serverID)
	plan.Rules = rulesForState(now, plan.Rules == nil)
	return nil
}

func (r *rootserverFirewallResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rootserverFirewallModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		addAPIError(&resp.Diagnostics, "Could not configure the server firewall", err, firewallRuleFields)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *rootserverFirewallResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state rootserverFirewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	serverID := state.ServerID.ValueString()
	if serverID == "" {
		serverID = state.ID.ValueString()
		state.ServerID = state.ID
	}
	have, err := r.rules(ctx, serverID)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read the server firewall", err, nil)
		return
	}
	state.ID = types.StringValue(serverID)
	state.Rules = rulesForState(have, state.Rules == nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *rootserverFirewallResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan rootserverFirewallModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.apply(ctx, &plan); err != nil {
		addAPIError(&resp.Diagnostics, "Could not configure the server firewall", err, firewallRuleFields)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *rootserverFirewallResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rootserverFirewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Rules = []firewallRuleModel{}
	if err := r.apply(ctx, &state); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not remove the server firewall rules", err, nil)
	}
}

func (r *rootserverFirewallResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("server_id"), req.ID)...)
}
