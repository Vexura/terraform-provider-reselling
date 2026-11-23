package provider

import (
	"context"
	"fmt"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

// firewallRuleModel is one rule of a server firewall or a firewall group. Rules are ordered:
// the first matching rule wins.
type firewallRuleModel struct {
	ID              types.String `tfsdk:"id"`
	Direction       types.String `tfsdk:"direction"`
	Action          types.String `tfsdk:"action"`
	Protocol        types.String `tfsdk:"protocol"`
	Source          types.String `tfsdk:"source"`
	SourcePort      types.String `tfsdk:"source_port"`
	Destination     types.String `tfsdk:"destination"`
	DestinationPort types.String `tfsdk:"destination_port"`
	Comment         types.String `tfsdk:"comment"`
	Enabled         types.Bool   `tfsdk:"enabled"`
}

type apiFirewallRule struct {
	ID              string  `json:"id"`
	Direction       string  `json:"direction"`
	Action          string  `json:"action"`
	Protocol        *string `json:"protocol"`
	Source          *string `json:"source"`
	SourcePort      *string `json:"source_port"`
	Destination     *string `json:"destination"`
	DestinationPort *string `json:"destination_port"`
	Comment         *string `json:"comment"`
	Enabled         *bool   `json:"enabled"`
	SortOrder       int64   `json:"sort_order"`
}

func firewallRuleSchema() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional:            true,
		MarkdownDescription: "Ordered rules; the first matching rule decides. Rules are matched to the existing ones by position.",
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"id": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseNonNullStateForUnknown()}},
				"direction": schema.StringAttribute{
					Required: true, MarkdownDescription: "`in` or `out`.",
					Validators: []validator.String{stringvalidator.OneOf("in", "out")},
				},
				"action": schema.StringAttribute{
					Required: true, MarkdownDescription: "`accept`, `drop` or `reject`.",
					Validators: []validator.String{stringvalidator.OneOf("accept", "drop", "reject")},
				},
				"protocol": schema.StringAttribute{
					Optional: true, MarkdownDescription: "`tcp`, `udp`, `icmp`, `icmpv6`, `gre`, `esp`, `ah`, `sctp`, `any` or `all`; any protocol when not set.",
					Validators: []validator.String{stringvalidator.OneOf("tcp", "udp", "icmp", "icmpv6", "gre", "esp", "ah", "sctp", "any", "all")},
				},
				"source":           schema.StringAttribute{Optional: true, MarkdownDescription: "Source address or network."},
				"source_port":      schema.StringAttribute{Optional: true},
				"destination":      schema.StringAttribute{Optional: true, MarkdownDescription: "Destination address or network."},
				"destination_port": schema.StringAttribute{Optional: true, MarkdownDescription: "Port, range (`8000:8100`) or list (`80,443`)."},
				"comment":          schema.StringAttribute{Optional: true},
				"enabled":          schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true)},
			},
		},
	}
}

func (m firewallRuleModel) body(withEnabled bool) map[string]any {
	body := map[string]any{
		"direction": m.Direction.ValueString(), "action": m.Action.ValueString(),
		"protocol": optString(m.Protocol), "source": optString(m.Source), "source_port": optString(m.SourcePort),
		"destination": optString(m.Destination), "destination_port": optString(m.DestinationPort), "comment": optString(m.Comment),
	}
	if withEnabled {
		body["is_enabled"] = m.Enabled.IsNull() || m.Enabled.IsUnknown() || m.Enabled.ValueBool()
	}
	return body
}

func ruleFromAPI(r apiFirewallRule) firewallRuleModel {
	enabled := true
	if r.Enabled != nil {
		enabled = *r.Enabled
	}
	return firewallRuleModel{
		ID: types.StringValue(r.ID), Direction: types.StringValue(r.Direction), Action: types.StringValue(r.Action),
		Protocol: strEmptyNull(r.Protocol), Source: strEmptyNull(r.Source), SourcePort: strEmptyNull(r.SourcePort),
		Destination: strEmptyNull(r.Destination), DestinationPort: strEmptyNull(r.DestinationPort), Comment: strEmptyNull(r.Comment),
		Enabled: types.BoolValue(enabled),
	}
}

// sameRule compares the configurable fields of a rule.
func sameRule(want firewallRuleModel, have apiFirewallRule) bool {
	got := ruleFromAPI(have)
	enabled := want.Enabled.IsNull() || want.Enabled.IsUnknown() || want.Enabled.ValueBool()
	return want.Direction.Equal(got.Direction) && want.Action.Equal(got.Action) && nullEq(want.Protocol, got.Protocol) &&
		nullEq(want.Source, got.Source) && nullEq(want.SourcePort, got.SourcePort) && nullEq(want.Destination, got.Destination) &&
		nullEq(want.DestinationPort, got.DestinationPort) && nullEq(want.Comment, got.Comment) && enabled == got.Enabled.ValueBool()
}

func nullEq(a, b types.String) bool {
	return a.ValueString() == b.ValueString()
}

func sortRules(rules []apiFirewallRule) {
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].SortOrder < rules[j].SortOrder })
}

// ruleEndpoints are the paths of one rule list (a server or a group).
type ruleEndpoints struct {
	create  string
	rule    func(id string) string // + "/update" or "/delete"
	reorder string
}

// reconcileRules makes the rule list equal to want: rules are updated by position, missing ones
// created at the end, surplus ones deleted, then the order is fixed if it still differs.
func reconcileRules(ctx context.Context, c *client.Client, ep ruleEndpoints, have []apiFirewallRule, want []firewallRuleModel, reread func(context.Context) ([]apiFirewallRule, error)) error {
	sortRules(have)
	for i, rule := range want {
		if i < len(have) {
			if sameRule(rule, have[i]) {
				continue
			}
			if err := c.Post(ctx, ep.rule(have[i].ID)+"/update", rule.body(true), nil); err != nil {
				return fmt.Errorf("rule %d: %w", i, err)
			}
			continue
		}
		var out struct {
			Rule apiFirewallRule `json:"rule"`
		}
		if err := c.Post(ctx, ep.create, rule.body(false), &out); err != nil {
			return fmt.Errorf("rule %d: %w", i, err)
		}
		if !rule.Enabled.IsNull() && !rule.Enabled.IsUnknown() && !rule.Enabled.ValueBool() {
			if err := c.Post(ctx, ep.rule(out.Rule.ID)+"/update", map[string]any{"is_enabled": false}, nil); err != nil {
				return fmt.Errorf("rule %d: %w", i, err)
			}
		}
	}
	for i := len(want); i < len(have); i++ {
		if err := c.Post(ctx, ep.rule(have[i].ID)+"/delete", map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
			return fmt.Errorf("deleting rule %s: %w", have[i].ID, err)
		}
	}

	now, err := reread(ctx)
	if err != nil {
		return err
	}
	sortRules(now)
	inOrder := len(now) == len(want)
	for i := range now {
		if !inOrder || !sameRule(want[i], now[i]) {
			inOrder = false
			break
		}
	}
	if inOrder || len(now) == 0 {
		return nil
	}
	// The API placed rules differently: put them into the configured order. Every configured
	// rule is matched to the first unused rule with the same content.
	used := map[string]bool{}
	ids := make([]string, 0, len(now))
	for _, w := range want {
		for _, h := range now {
			if !used[h.ID] && sameRule(w, h) {
				used[h.ID] = true
				ids = append(ids, h.ID)
				break
			}
		}
	}
	if len(ids) != len(now) {
		return fmt.Errorf("the firewall rules returned by the API do not match the configuration after the update")
	}
	return c.Post(ctx, ep.reorder, map[string]any{"rule_ids": ids}, nil)
}

// rulesForState converts the API rules into the model list, sorted.
func rulesForState(have []apiFirewallRule, configuredNull bool) []firewallRuleModel {
	sortRules(have)
	if len(have) == 0 && configuredNull {
		return nil
	}
	out := make([]firewallRuleModel, 0, len(have))
	for _, r := range have {
		out = append(out, ruleFromAPI(r))
	}
	return out
}

var firewallRuleFields = fieldPaths{
	"direction": path.Root("rules"), "action": path.Root("rules"), "protocol": path.Root("rules"), "source": path.Root("rules"),
	"source_port": path.Root("rules"), "destination": path.Root("rules"), "destination_port": path.Root("rules"), "comment": path.Root("rules"),
}
