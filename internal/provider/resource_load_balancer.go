package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

const lbPath = "/products/loadbalancer"

// managedGone reports statuses of managed instances (load balancers, databases) that mean "deleted".
func managedGone(status string) bool {
	return status == "pending_deletion" || status == "deleted"
}

// --- load balancer ---

var (
	_ resource.Resource                = (*loadBalancerResource)(nil)
	_ resource.ResourceWithImportState = (*loadBalancerResource)(nil)
)

type loadBalancerResource struct{ base }

// NewLoadBalancerResource manages a load balancer.
func NewLoadBalancerResource() resource.Resource { return &loadBalancerResource{} }

type loadBalancerModel struct {
	ID          types.String   `tfsdk:"id"`
	Name        types.String   `tfsdk:"name"`
	LocationID  types.String   `tfsdk:"location_id"`
	VPCID       types.String   `tfsdk:"vpc_id"`
	PlanID      types.String   `tfsdk:"plan_id"`
	EnableIPv6  types.Bool     `tfsdk:"enable_ipv6"`
	Status      types.String   `tfsdk:"status"`
	IPv4Address types.String   `tfsdk:"ipv4_address"`
	IPv6Address types.String   `tfsdk:"ipv6_address"`
	PrivateIP   types.String   `tfsdk:"private_ip"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

type apiLoadBalancer struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Status       string  `json:"status"`
	IPv4Address  *string `json:"ipv4_address"`
	IPv6Address  *string `json:"ipv6_address"`
	PrivateIP    *string `json:"private_ip"`
	Location     *idRef  `json:"location"`
	Plan         *idRef  `json:"plan"`
	VPC          *idRef  `json:"vpc"`
	ErrorMessage *string `json:"error_message"`
}

var lbFields = fieldPaths{"name": path.Root("name"), "location_id": path.Root("location_id"), "vpc_id": path.Root("vpc_id"), "plan_id": path.Root("plan_id"), "enable_ipv6": path.Root("enable_ipv6")}

func (r *loadBalancerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer"
}

func (r *loadBalancerResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A load balancer in a VPC with a public IPv4 address. Services (listeners), targets and certificates are " +
			"separate resources. Deleting moves it to the trash for 48 hours.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Name. Changed in place."},
			"location_id": schema.StringAttribute{Required: true, PlanModifiers: replace},
			"vpc_id":      schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "VPC the targets are reached through."},
			"plan_id":     schema.StringAttribute{Required: true, MarkdownDescription: "Plan (see `reselling_load_balancer_plans`). Changed in place; a smaller plan must still fit the configuration."},
			"enable_ipv6": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.RequiresReplace()},
			},
			"status":       schema.StringAttribute{Computed: true},
			"ipv4_address": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"ipv6_address": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"private_ip":   schema.StringAttribute{Computed: true, PlanModifiers: keep},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

func (m *loadBalancerModel) apply(lb apiLoadBalancer) {
	m.ID = types.StringValue(lb.ID)
	m.Name = types.StringValue(lb.Name)
	if lb.Location != nil {
		m.LocationID = types.StringValue(lb.Location.ID)
	}
	if lb.VPC != nil {
		m.VPCID = types.StringValue(lb.VPC.ID)
	}
	if lb.Plan != nil {
		m.PlanID = types.StringValue(lb.Plan.ID)
	}
	m.Status = types.StringValue(lb.Status)
	m.IPv4Address = strOrNull(lb.IPv4Address)
	m.IPv6Address = strOrNull(lb.IPv6Address)
	m.PrivateIP = strOrNull(lb.PrivateIP)
	if m.EnableIPv6.IsNull() || m.EnableIPv6.IsUnknown() || lb.IPv6Address != nil {
		m.EnableIPv6 = types.BoolValue(lb.IPv6Address != nil)
	}
}

func (r *loadBalancerResource) get(ctx context.Context, id string) (apiLoadBalancer, error) {
	var out apiLoadBalancer
	err := r.client.Get(ctx, lbPath+"/"+id, &out)
	return out, err
}

func (r *loadBalancerResource) waitRunning(ctx context.Context, id string, timeout time.Duration) (apiLoadBalancer, error) {
	var lb apiLoadBalancer
	err := waitFor(ctx, timeout, "load balancer "+id+" to run", func(ctx context.Context) (bool, error) {
		var err error
		lb, err = r.get(ctx, id)
		if err != nil {
			return false, err
		}
		switch lb.Status {
		case "running", "stopped":
			return true, nil
		case "error":
			msg := "load balancer is in status error"
			if lb.ErrorMessage != nil {
				msg += ": " + *lb.ErrorMessage
			}
			return false, fmt.Errorf("%s", msg)
		}
		if managedGone(lb.Status) {
			return false, errGone
		}
		return false, nil
	})
	return lb, err
}

func (r *loadBalancerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan loadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"name": plan.Name.ValueString(), "location_id": plan.LocationID.ValueString(), "vpc_id": plan.VPCID.ValueString(),
		"plan_id": plan.PlanID.ValueString(), "enable_ipv6": plan.EnableIPv6.ValueBool(),
	}
	var out apiLoadBalancer
	if err := r.client.Post(ctx, lbPath+"/create", body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not order load balancer", err, lbFields)
		return
	}
	plan.apply(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

	lb, err := r.waitRunning(ctx, out.ID, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Load balancer provisioning did not finish", err.Error())
		return
	}
	plan.apply(lb)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *loadBalancerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state loadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	lb, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read load balancer", err, nil)
		return
	}
	if managedGone(lb.Status) {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(lb)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *loadBalancerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state loadBalancerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{}
	if !plan.Name.Equal(state.Name) {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.PlanID.Equal(state.PlanID) {
		body["plan_id"] = plan.PlanID.ValueString()
	}
	id := state.ID.ValueString()
	if len(body) > 0 {
		if err := r.client.Post(ctx, lbPath+"/"+id+"/update", body, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not update load balancer", err, lbFields)
			return
		}
	}
	lb, err := r.waitRunning(ctx, id, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Load balancer update did not finish", err.Error())
		return
	}
	plan.apply(lb)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *loadBalancerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state loadBalancerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, lbPath+"/"+state.ID.ValueString()+"/delete", map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete load balancer", err, nil)
	}
}

func (r *loadBalancerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// --- service (listener) ---

var (
	_ resource.Resource                = (*lbServiceResource)(nil)
	_ resource.ResourceWithImportState = (*lbServiceResource)(nil)
)

type lbServiceResource struct{ base }

// NewLoadBalancerServiceResource manages a service (listener) of a load balancer.
func NewLoadBalancerServiceResource() resource.Resource { return &lbServiceResource{} }

type lbServiceModel struct {
	ID                        types.String `tfsdk:"id"`
	LoadBalancerID            types.String `tfsdk:"load_balancer_id"`
	Name                      types.String `tfsdk:"name"`
	Protocol                  types.String `tfsdk:"protocol"`
	ListenPort                types.Int64  `tfsdk:"listen_port"`
	TargetPort                types.Int64  `tfsdk:"target_port"`
	Algorithm                 types.String `tfsdk:"algorithm"`
	StickySessions            types.Bool   `tfsdk:"sticky_sessions"`
	StickyCookieName          types.String `tfsdk:"sticky_cookie_name"`
	ProxyProtocol             types.String `tfsdk:"proxy_protocol"`
	HealthCheckProtocol       types.String `tfsdk:"health_check_protocol"`
	HealthCheckPath           types.String `tfsdk:"health_check_path"`
	HealthCheckExpectedStatus types.String `tfsdk:"health_check_expected_status"`
	HealthCheckInterval       types.Int64  `tfsdk:"health_check_interval"`
	HealthCheckTimeout        types.Int64  `tfsdk:"health_check_timeout"`
	HealthCheckRise           types.Int64  `tfsdk:"health_check_rise"`
	HealthCheckFall           types.Int64  `tfsdk:"health_check_fall"`
	TimeoutConnect            types.Int64  `tfsdk:"timeout_connect"`
	TimeoutClient             types.Int64  `tfsdk:"timeout_client"`
	TimeoutServer             types.Int64  `tfsdk:"timeout_server"`
	RedirectToHTTPS           types.Bool   `tfsdk:"redirect_to_https"`
	ForwardHeaders            types.Bool   `tfsdk:"forward_headers"`
	CertificateIDs            types.Set    `tfsdk:"certificate_ids"`
	DefaultCertificateID      types.String `tfsdk:"default_certificate_id"`
}

type apiLBService struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	Protocol                  string   `json:"protocol"`
	ListenPort                int64    `json:"listen_port"`
	TargetPort                int64    `json:"target_port"`
	Algorithm                 *string  `json:"algorithm"`
	StickySessions            *bool    `json:"sticky_sessions"`
	StickyCookieName          *string  `json:"sticky_cookie_name"`
	ProxyProtocol             *string  `json:"proxy_protocol"`
	HealthCheckProtocol       *string  `json:"health_check_protocol"`
	HealthCheckPath           *string  `json:"health_check_path"`
	HealthCheckExpectedStatus *string  `json:"health_check_expected_status"`
	HealthCheckInterval       *int64   `json:"health_check_interval"`
	HealthCheckTimeout        *int64   `json:"health_check_timeout"`
	HealthCheckRise           *int64   `json:"health_check_rise"`
	HealthCheckFall           *int64   `json:"health_check_fall"`
	TimeoutConnect            *int64   `json:"timeout_connect"`
	TimeoutClient             *int64   `json:"timeout_client"`
	TimeoutServer             *int64   `json:"timeout_server"`
	RedirectToHTTPS           *bool    `json:"redirect_to_https"`
	ForwardHeaders            *bool    `json:"forward_headers"`
	CertificateIDs            []string `json:"certificate_ids"`
	DefaultCertificateID      *string  `json:"default_certificate_id"`
}

var lbServiceFieldNames = []string{
	"name", "protocol", "listen_port", "target_port", "algorithm", "sticky_sessions", "sticky_cookie_name", "proxy_protocol",
	"health_check_protocol", "health_check_path", "health_check_expected_status", "health_check_interval", "health_check_timeout",
	"health_check_rise", "health_check_fall", "timeout_connect", "timeout_client", "timeout_server", "redirect_to_https",
	"forward_headers", "certificate_ids", "default_certificate_id",
}

func lbServiceFields() fieldPaths {
	out := fieldPaths{}
	for _, n := range lbServiceFieldNames {
		out[n] = path.Root(n)
	}
	return out
}

func (r *lbServiceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_service"
}

func (r *lbServiceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	optStr := func(desc string, values ...string) schema.StringAttribute {
		a := schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: desc, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}}
		if len(values) > 0 {
			a.Validators = []validator.String{stringvalidator.OneOf(values...)}
		}
		return a
	}
	optInt := func(desc string, min, max int64) schema.Int64Attribute {
		return schema.Int64Attribute{
			Optional: true, Computed: true, MarkdownDescription: desc,
			PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			Validators:    []validator.Int64{int64validator.Between(min, max)},
		}
	}
	optBool := func(desc string) schema.BoolAttribute {
		return schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: desc, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A service (listener) of a load balancer: a port it listens on (`tcp`, `http`, or `https` with TLS termination) " +
			"and how it forwards to the targets. Unset optional values take the platform defaults. Import with `load_balancer_id/service_id`.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"load_balancer_id": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"name":             schema.StringAttribute{Required: true},
			"protocol": schema.StringAttribute{
				Required: true, MarkdownDescription: "`tcp`, `http` or `https`.",
				Validators: []validator.String{stringvalidator.OneOf("tcp", "http", "https")},
			},
			"listen_port":                  schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 65535)}},
			"target_port":                  schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 65535)}},
			"algorithm":                    optStr("`roundrobin`, `leastconn` or `source`.", "roundrobin", "leastconn", "source"),
			"sticky_sessions":              optBool("Pin a client to one target with a cookie (http/https)."),
			"sticky_cookie_name":           optStr("Name of the sticky cookie."),
			"proxy_protocol":               optStr("`none`, `v1` or `v2`.", "none", "v1", "v2"),
			"health_check_protocol":        optStr("`tcp` or `http`.", "tcp", "http"),
			"health_check_path":            optStr("Path for http health checks, for example `/health`."),
			"health_check_expected_status": optStr("Expected status codes such as `200`, `200,204` or `2xx`."),
			"health_check_interval":        optInt("Seconds between checks (1-300).", 1, 300),
			"health_check_timeout":         optInt("Seconds (1-60).", 1, 60),
			"health_check_rise":            optInt("Successful checks until healthy (1-10).", 1, 10),
			"health_check_fall":            optInt("Failed checks until unhealthy (1-10).", 1, 10),
			"timeout_connect":              optInt("Seconds (1-300).", 1, 300),
			"timeout_client":               optInt("Seconds (1-3600).", 1, 3600),
			"timeout_server":               optInt("Seconds (1-3600).", 1, 3600),
			"redirect_to_https":            optBool("Redirect http to https."),
			"forward_headers":              optBool("Send X-Forwarded-For and X-Forwarded-Proto."),
			"certificate_ids": schema.SetAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				MarkdownDescription: "Certificates (`reselling_load_balancer_certificate`) served by an https service.",
				PlanModifiers:       []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
			},
			"default_certificate_id": optStr("Certificate served when no SNI name matches."),
		},
	}
}

func (m lbServiceModel) body() map[string]any {
	body := map[string]any{
		"name": m.Name.ValueString(), "protocol": m.Protocol.ValueString(),
		"listen_port": m.ListenPort.ValueInt64(), "target_port": m.TargetPort.ValueInt64(),
	}
	for k, v := range map[string]types.String{
		"algorithm": m.Algorithm, "sticky_cookie_name": m.StickyCookieName, "proxy_protocol": m.ProxyProtocol,
		"health_check_protocol": m.HealthCheckProtocol, "health_check_path": m.HealthCheckPath,
		"health_check_expected_status": m.HealthCheckExpectedStatus, "default_certificate_id": m.DefaultCertificateID,
	} {
		if p := optString(v); p != nil {
			body[k] = *p
		}
	}
	for k, v := range map[string]types.Int64{
		"health_check_interval": m.HealthCheckInterval, "health_check_timeout": m.HealthCheckTimeout, "health_check_rise": m.HealthCheckRise,
		"health_check_fall": m.HealthCheckFall, "timeout_connect": m.TimeoutConnect, "timeout_client": m.TimeoutClient, "timeout_server": m.TimeoutServer,
	} {
		if p := optInt(v); p != nil {
			body[k] = *p
		}
	}
	for k, v := range map[string]types.Bool{"sticky_sessions": m.StickySessions, "redirect_to_https": m.RedirectToHTTPS, "forward_headers": m.ForwardHeaders} {
		if p := optBool(v); p != nil {
			body[k] = *p
		}
	}
	if !m.CertificateIDs.IsNull() && !m.CertificateIDs.IsUnknown() {
		ids := []string{}
		for _, e := range m.CertificateIDs.Elements() {
			if s, ok := e.(types.String); ok {
				ids = append(ids, s.ValueString())
			}
		}
		body["certificate_ids"] = ids
	}
	return body
}

func (m *lbServiceModel) apply(s apiLBService) {
	m.ID = types.StringValue(s.ID)
	m.Name = types.StringValue(s.Name)
	m.Protocol = types.StringValue(s.Protocol)
	m.ListenPort = types.Int64Value(s.ListenPort)
	m.TargetPort = types.Int64Value(s.TargetPort)
	m.Algorithm = strOrNull(s.Algorithm)
	m.StickySessions = boolOrNull(s.StickySessions)
	m.StickyCookieName = strOrNull(s.StickyCookieName)
	m.ProxyProtocol = strOrNull(s.ProxyProtocol)
	m.HealthCheckProtocol = strOrNull(s.HealthCheckProtocol)
	m.HealthCheckPath = strOrNull(s.HealthCheckPath)
	m.HealthCheckExpectedStatus = strOrNull(s.HealthCheckExpectedStatus)
	m.HealthCheckInterval = intOrNull(s.HealthCheckInterval)
	m.HealthCheckTimeout = intOrNull(s.HealthCheckTimeout)
	m.HealthCheckRise = intOrNull(s.HealthCheckRise)
	m.HealthCheckFall = intOrNull(s.HealthCheckFall)
	m.TimeoutConnect = intOrNull(s.TimeoutConnect)
	m.TimeoutClient = intOrNull(s.TimeoutClient)
	m.TimeoutServer = intOrNull(s.TimeoutServer)
	m.RedirectToHTTPS = boolOrNull(s.RedirectToHTTPS)
	m.ForwardHeaders = boolOrNull(s.ForwardHeaders)
	if s.CertificateIDs == nil {
		s.CertificateIDs = []string{}
	}
	m.CertificateIDs = stringSet(s.CertificateIDs)
	m.DefaultCertificateID = strOrNull(s.DefaultCertificateID)
}

func (r *lbServiceResource) find(ctx context.Context, lbID, id string) (*apiLBService, error) {
	var out struct {
		Listeners []apiLBService `json:"listeners"`
	}
	if err := r.client.Get(ctx, lbPath+"/"+lbID+"/listeners", &out); err != nil {
		return nil, err
	}
	for i := range out.Listeners {
		if out.Listeners[i].ID == id {
			return &out.Listeners[i], nil
		}
	}
	return nil, nil
}

func (r *lbServiceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbServiceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiLBService
	if err := r.client.Post(ctx, lbPath+"/"+plan.LoadBalancerID.ValueString()+"/listeners/create", plan.body(), &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create load balancer service", err, lbServiceFields())
		return
	}
	plan.apply(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *lbServiceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbServiceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.find(ctx, state.LoadBalancerID.ValueString(), state.ID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not read load balancer services", err, nil)
		return
	}
	if s == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(*s)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *lbServiceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbServiceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiLBService
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/listeners/%s/update", lbPath, plan.LoadBalancerID.ValueString(), plan.ID.ValueString()), plan.body(), &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not update load balancer service", err, lbServiceFields())
		return
	}
	plan.apply(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *lbServiceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbServiceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/listeners/%s/delete", lbPath, state.LoadBalancerID.ValueString(), state.ID.ValueString()), map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete load balancer service", err, nil)
	}
}

func (r *lbServiceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importChild(ctx, req, resp, "load_balancer_id", "load_balancer_id/service_id")
}

// importChild handles "parent_id/child_id" import ids.
func importChild(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse, parentAttr, format string) {
	parts, err := splitID(req.ID, 2, format)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(parentAttr), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

// --- target ---

var (
	_ resource.Resource                = (*lbTargetResource)(nil)
	_ resource.ResourceWithImportState = (*lbTargetResource)(nil)
)

type lbTargetResource struct{ base }

// NewLoadBalancerTargetResource manages a target of a load balancer.
func NewLoadBalancerTargetResource() resource.Resource { return &lbTargetResource{} }

type lbTargetModel struct {
	ID             types.String `tfsdk:"id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	Type           types.String `tfsdk:"type"`
	RootserverID   types.String `tfsdk:"rootserver_id"`
	Label          types.String `tfsdk:"label"`
	IPAddress      types.String `tfsdk:"ip_address"`
	Port           types.Int64  `tfsdk:"port"`
	Weight         types.Int64  `tfsdk:"weight"`
	Health         types.String `tfsdk:"health"`
	Members        types.List   `tfsdk:"members"`
}

type apiLBTarget struct {
	ID           string  `json:"id"`
	Type         string  `json:"type"`
	RootserverID *string `json:"rootserver_id"`
	Label        *string `json:"label"`
	IPAddress    *string `json:"ip_address"`
	Port         *int64  `json:"port"`
	Weight       *int64  `json:"weight"`
	Health       any     `json:"health"`
	Members      []struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"members"`
}

func (r *lbTargetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_target"
}

func (r *lbTargetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A target of a load balancer: a rootserver of its VPC (`server`), every server of the VPC carrying a label (`label`), " +
			"or an address in the VPC (`ip`). Import with `load_balancer_id/target_id`.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"load_balancer_id": schema.StringAttribute{Required: true, PlanModifiers: replace},
			"type": schema.StringAttribute{
				Required: true, PlanModifiers: replace, MarkdownDescription: "`server`, `label` or `ip`.",
				Validators: []validator.String{stringvalidator.OneOf("server", "label", "ip")},
			},
			"rootserver_id": schema.StringAttribute{Optional: true, PlanModifiers: replace, MarkdownDescription: "For type `server`."},
			"label":         schema.StringAttribute{Optional: true, PlanModifiers: replace, MarkdownDescription: "For type `label`."},
			"ip_address":    schema.StringAttribute{Optional: true, PlanModifiers: replace, MarkdownDescription: "For type `ip`."},
			"port": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Overrides the target port of the services.",
				Validators: []validator.Int64{int64validator.Between(1, 65535)},
			},
			"weight": schema.Int64Attribute{
				Optional: true, Computed: true, MarkdownDescription: "Weight 1-256 (default 100).",
				Validators:    []validator.Int64{int64validator.Between(1, 256)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"health":  schema.StringAttribute{Computed: true, MarkdownDescription: "Health summary reported by the load balancer."},
			"members": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Resolved addresses."},
		},
	}
}

func (m *lbTargetModel) apply(t apiLBTarget) {
	m.ID = types.StringValue(t.ID)
	m.Type = types.StringValue(t.Type)
	m.RootserverID = strOrNull(t.RootserverID)
	m.Label = strOrNull(t.Label)
	m.IPAddress = strOrNull(t.IPAddress)
	m.Port = intOrNull(t.Port)
	m.Weight = intOrNull(t.Weight)
	switch h := t.Health.(type) {
	case string:
		m.Health = types.StringValue(h)
	case nil:
		m.Health = types.StringNull()
	default:
		m.Health = types.StringValue(fmt.Sprint(h))
	}
	addrs := []string{}
	for _, mem := range t.Members {
		addrs = append(addrs, mem.Address)
	}
	m.Members = stringList(addrs)
}

func (r *lbTargetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"type": plan.Type.ValueString(), "rootserver_id": optString(plan.RootserverID), "label": optString(plan.Label),
		"ip_address": optString(plan.IPAddress), "port": optInt(plan.Port), "weight": optInt(plan.Weight),
	}
	var out apiLBTarget
	fields := fieldPaths{"type": path.Root("type"), "rootserver_id": path.Root("rootserver_id"), "label": path.Root("label"), "ip_address": path.Root("ip_address"), "port": path.Root("port"), "weight": path.Root("weight")}
	if err := r.client.Post(ctx, lbPath+"/"+plan.LoadBalancerID.ValueString()+"/targets/create", body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create load balancer target", err, fields)
		return
	}
	plan.apply(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *lbTargetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Targets []apiLBTarget `json:"targets"`
	}
	if err := r.client.Get(ctx, lbPath+"/"+state.LoadBalancerID.ValueString()+"/targets", &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read load balancer targets", err, nil)
		return
	}
	for _, t := range out.Targets {
		if t.ID == state.ID.ValueString() {
			state.apply(t)
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *lbTargetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan lbTargetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"type": plan.Type.ValueString(), "rootserver_id": optString(plan.RootserverID), "label": optString(plan.Label),
		"ip_address": optString(plan.IPAddress), "port": optInt(plan.Port), "weight": optInt(plan.Weight),
	}
	var out apiLBTarget
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/targets/%s/update", lbPath, plan.LoadBalancerID.ValueString(), plan.ID.ValueString()), body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not update load balancer target", err, fieldPaths{"port": path.Root("port"), "weight": path.Root("weight")})
		return
	}
	plan.apply(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *lbTargetResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbTargetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/targets/%s/delete", lbPath, state.LoadBalancerID.ValueString(), state.ID.ValueString()), map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete load balancer target", err, nil)
	}
}

func (r *lbTargetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importChild(ctx, req, resp, "load_balancer_id", "load_balancer_id/target_id")
}

// --- certificate ---

var (
	_ resource.Resource                = (*lbCertificateResource)(nil)
	_ resource.ResourceWithImportState = (*lbCertificateResource)(nil)
)

type lbCertificateResource struct{ base }

// NewLoadBalancerCertificateResource manages a TLS certificate of a load balancer.
func NewLoadBalancerCertificateResource() resource.Resource { return &lbCertificateResource{} }

type lbCertificateModel struct {
	ID             types.String `tfsdk:"id"`
	LoadBalancerID types.String `tfsdk:"load_balancer_id"`
	Type           types.String `tfsdk:"type"`
	Name           types.String `tfsdk:"name"`
	Domains        types.List   `tfsdk:"domains"`
	Certificate    types.String `tfsdk:"certificate"`
	PrivateKey     types.String `tfsdk:"private_key"`
	Chain          types.String `tfsdk:"chain"`
	Status         types.String `tfsdk:"status"`
	Fingerprint    types.String `tfsdk:"fingerprint"`
	ExpiresAt      types.String `tfsdk:"expires_at"`
}

type apiLBCertificate struct {
	ID          string   `json:"id"`
	Name        *string  `json:"name"`
	Type        string   `json:"type"`
	Domains     []string `json:"domains"`
	Status      string   `json:"status"`
	Fingerprint *string  `json:"fingerprint"`
	ExpiresAt   *string  `json:"expires_at"`
}

func (r *lbCertificateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_load_balancer_certificate"
}

func (r *lbCertificateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A TLS certificate of a load balancer: `managed` (Let's Encrypt for `domains`, renewed automatically) or " +
			"`custom` (your PEM certificate and key). There is no update endpoint: every change replaces the certificate. " +
			"The PEM fields are not returned by the API; after an import they are empty. Import with `load_balancer_id/certificate_id`.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"load_balancer_id": schema.StringAttribute{Required: true, PlanModifiers: replace},
			"type": schema.StringAttribute{
				Required: true, PlanModifiers: replace, MarkdownDescription: "`managed` or `custom`.",
				Validators: []validator.String{stringvalidator.OneOf("managed", "custom")},
			},
			"name": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()}},
			"domains": schema.ListAttribute{
				Optional: true, Computed: true, ElementType: types.StringType,
				MarkdownDescription: "Domains of a managed certificate (max. 10); they must point to the load balancer unless their DNS zone is hosted on the platform.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.RequiresReplaceIfConfigured(), listplanmodifier.UseStateForUnknown()},
			},
			"certificate": schema.StringAttribute{Optional: true, PlanModifiers: replace, MarkdownDescription: "PEM certificate (custom)."},
			"private_key": schema.StringAttribute{Optional: true, Sensitive: true, PlanModifiers: replace, MarkdownDescription: "Unencrypted PEM private key (custom)."},
			"chain":       schema.StringAttribute{Optional: true, PlanModifiers: replace, MarkdownDescription: "PEM intermediates, issuer of the certificate first (custom)."},
			"status":      schema.StringAttribute{Computed: true},
			"fingerprint": schema.StringAttribute{Computed: true},
			"expires_at":  schema.StringAttribute{Computed: true},
		},
	}
}

func (m *lbCertificateModel) apply(ctx context.Context, c apiLBCertificate) {
	m.ID = types.StringValue(c.ID)
	m.Type = types.StringValue(c.Type)
	m.Name = strOrNull(c.Name)
	m.Domains = keepListIfSameSet(ctx, m.Domains, c.Domains)
	if m.Domains.IsUnknown() {
		m.Domains = types.ListNull(types.StringType)
	}
	m.Status = types.StringValue(c.Status)
	m.Fingerprint = strOrNull(c.Fingerprint)
	m.ExpiresAt = strOrNull(c.ExpiresAt)
}

func (r *lbCertificateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan lbCertificateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"type": plan.Type.ValueString()}
	for k, v := range map[string]types.String{"name": plan.Name, "certificate": plan.Certificate, "private_key": plan.PrivateKey, "chain": plan.Chain} {
		if p := optString(v); p != nil {
			body[k] = *p
		}
	}
	if domains := listStrings(ctx, plan.Domains, &resp.Diagnostics); len(domains) > 0 {
		body["domains"] = domains
	}
	fields := fieldPaths{"type": path.Root("type"), "name": path.Root("name"), "domains": path.Root("domains"), "certificate": path.Root("certificate"), "private_key": path.Root("private_key"), "chain": path.Root("chain")}
	var out apiLBCertificate
	if err := r.client.Post(ctx, lbPath+"/"+plan.LoadBalancerID.ValueString()+"/certificates/create", body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create load balancer certificate", err, fields)
		return
	}
	plan.apply(ctx, out)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *lbCertificateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state lbCertificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Certificates []apiLBCertificate `json:"certificates"`
	}
	if err := r.client.Get(ctx, lbPath+"/"+state.LoadBalancerID.ValueString()+"/certificates", &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read load balancer certificates", err, nil)
		return
	}
	for _, c := range out.Certificates {
		if c.ID == state.ID.ValueString() {
			state.apply(ctx, c)
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *lbCertificateResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected update", "Certificates cannot be changed in place; every change replaces them.")
}

func (r *lbCertificateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state lbCertificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/certificates/%s/delete", lbPath, state.LoadBalancerID.ValueString(), state.ID.ValueString()), map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete load balancer certificate", err, nil)
	}
}

func (r *lbCertificateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importChild(ctx, req, resp, "load_balancer_id", "load_balancer_id/certificate_id")
}
