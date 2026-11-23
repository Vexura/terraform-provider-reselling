package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

const vpcPath = "/products/rootserver/vpc"

// --- VPC ---

var (
	_ resource.Resource                = (*vpcResource)(nil)
	_ resource.ResourceWithImportState = (*vpcResource)(nil)
)

type vpcResource struct{ base }

// NewVPCResource manages a private network.
func NewVPCResource() resource.Resource { return &vpcResource{} }

type vpcModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Description  types.String `tfsdk:"description"`
	Subnet       types.String `tfsdk:"subnet"`
	LocationID   types.String `tfsdk:"location_id"`
	Gateway      types.String `tfsdk:"gateway"`
	PrefixLength types.Int64  `tfsdk:"prefix_length"`
}

type apiVPC struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  *string `json:"description"`
	Subnet       string  `json:"subnet"`
	LocationID   string  `json:"location_id"`
	Gateway      *string `json:"gateway"`
	PrefixLength int64   `json:"prefix_length"`
}

var vpcFields = fieldPaths{"name": path.Root("name"), "description": path.Root("description"), "subnet": path.Root("subnet"), "location_id": path.Root("location_id")}

func (r *vpcResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc"
}

func (r *vpcResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A private network (VPC) at one location. Servers join it with `reselling_vpc_member`, static routes are `reselling_vpc_route`.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Name. Changed in place."},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Description. Changed in place."},
			"subnet": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "Private (RFC 1918) network in CIDR notation between /16 and /29, for example `10.20.0.0/24`. Changing it replaces the VPC.",
			},
			"location_id":   schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Location (see `reselling_locations`)."},
			"gateway":       schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"prefix_length": schema.Int64Attribute{Computed: true},
		},
	}
}

func (m *vpcModel) apply(v apiVPC) {
	m.ID = types.StringValue(v.ID)
	m.Name = types.StringValue(v.Name)
	m.Description = strEmptyNull(v.Description)
	m.Subnet = types.StringValue(v.Subnet)
	m.LocationID = types.StringValue(v.LocationID)
	m.Gateway = strOrNull(v.Gateway)
	m.PrefixLength = types.Int64Value(v.PrefixLength)
}

func (r *vpcResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "subnet": plan.Subnet.ValueString(), "location_id": plan.LocationID.ValueString()}
	if v := optString(plan.Description); v != nil {
		body["description"] = *v
	}
	var out struct {
		VPC apiVPC `json:"vpc"`
	}
	if err := r.client.Post(ctx, vpcPath+"/create", body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create VPC", err, vpcFields)
		return
	}
	plan.apply(out.VPC)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *vpcResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiVPC
	if err := r.client.Get(ctx, vpcPath+"/"+state.ID.ValueString(), &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read VPC", err, nil)
		return
	}
	state.apply(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *vpcResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		VPC apiVPC `json:"vpc"`
	}
	body := map[string]any{"name": plan.Name.ValueString(), "description": optString(plan.Description)}
	if err := r.client.Post(ctx, vpcPath+"/"+plan.ID.ValueString()+"/update", body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not update VPC", err, vpcFields)
		return
	}
	plan.apply(out.VPC)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *vpcResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, vpcPath+"/"+state.ID.ValueString()+"/delete", map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete VPC", err, nil)
	}
}

func (r *vpcResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// --- route ---

var (
	_ resource.Resource                = (*vpcRouteResource)(nil)
	_ resource.ResourceWithImportState = (*vpcRouteResource)(nil)
)

type vpcRouteResource struct{ base }

// NewVPCRouteResource manages a static route of a VPC.
func NewVPCRouteResource() resource.Resource { return &vpcRouteResource{} }

type vpcRouteModel struct {
	ID          types.String `tfsdk:"id"`
	VPCID       types.String `tfsdk:"vpc_id"`
	Destination types.String `tfsdk:"destination"`
	NextHop     types.String `tfsdk:"next_hop"`
	Description types.String `tfsdk:"description"`
}

type apiVPCRoute struct {
	ID          string  `json:"id"`
	Destination string  `json:"destination"`
	NextHop     string  `json:"next_hop"`
	Description *string `json:"description"`
}

var vpcRouteFields = fieldPaths{"destination": path.Root("destination"), "next_hop": path.Root("next_hop"), "description": path.Root("description")}

func (r *vpcRouteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_route"
}

func (r *vpcRouteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A static route of a VPC: `destination` via the private IP of a member. Routes are stored and shown with guest " +
			"commands; they are not pushed into the guests automatically. Import with `vpc_id/route_id`.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"vpc_id":      schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"destination": schema.StringAttribute{Required: true, MarkdownDescription: "Destination network in CIDR notation, outside the VPC and not 0.0.0.0/0."},
			"next_hop":    schema.StringAttribute{Required: true, MarkdownDescription: "Private IP of a member of the VPC."},
			"description": schema.StringAttribute{Optional: true},
		},
	}
}

func (m *vpcRouteModel) apply(rt apiVPCRoute) {
	m.ID = types.StringValue(rt.ID)
	m.Destination = types.StringValue(rt.Destination)
	m.NextHop = types.StringValue(rt.NextHop)
	m.Description = strEmptyNull(rt.Description)
}

func (m vpcRouteModel) body() map[string]any {
	return map[string]any{"destination": m.Destination.ValueString(), "next_hop": m.NextHop.ValueString(), "description": optString(m.Description)}
}

func (r *vpcRouteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcRouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Route apiVPCRoute `json:"route"`
	}
	if err := r.client.Post(ctx, vpcPath+"/"+plan.VPCID.ValueString()+"/routes", plan.body(), &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create VPC route", err, vpcRouteFields)
		return
	}
	plan.apply(out.Route)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *vpcRouteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Routes []apiVPCRoute `json:"routes"`
	}
	if err := r.client.Get(ctx, vpcPath+"/"+state.VPCID.ValueString()+"/routes", &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read VPC routes", err, nil)
		return
	}
	for _, rt := range out.Routes {
		if rt.ID == state.ID.ValueString() {
			state.apply(rt)
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *vpcRouteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan vpcRouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Route apiVPCRoute `json:"route"`
	}
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/routes/%s/update", vpcPath, plan.VPCID.ValueString(), plan.ID.ValueString()), plan.body(), &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not update VPC route", err, vpcRouteFields)
		return
	}
	plan.apply(out.Route)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *vpcRouteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/routes/%s/delete", vpcPath, state.VPCID.ValueString(), state.ID.ValueString()), map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete VPC route", err, nil)
	}
}

func (r *vpcRouteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitID(req.ID, 2, "vpc_id/route_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}

// --- member ---

var (
	_ resource.Resource                = (*vpcMemberResource)(nil)
	_ resource.ResourceWithImportState = (*vpcMemberResource)(nil)
)

type vpcMemberResource struct{ base }

// NewVPCMemberResource joins a rootserver to a VPC.
func NewVPCMemberResource() resource.Resource { return &vpcMemberResource{} }

type vpcMemberModel struct {
	ID             types.String   `tfsdk:"id"`
	VPCID          types.String   `tfsdk:"vpc_id"`
	ServerID       types.String   `tfsdk:"server_id"`
	PrivateIP      types.String   `tfsdk:"private_ip"`
	AllowRestart   types.Bool     `tfsdk:"allow_restart"`
	NIC            types.String   `tfsdk:"nic"`
	GuestInterface types.String   `tfsdk:"guest_interface"`
	Status         types.String   `tfsdk:"status"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

type apiVPCMember struct {
	ID             string  `json:"id"`
	MemberType     string  `json:"member_type"`
	MemberID       string  `json:"member_id"`
	Status         string  `json:"status"`
	PrivateIP      *string `json:"private_ip"`
	NIC            *string `json:"nic"`
	GuestInterface *string `json:"guest_interface"`
	LastError      *string `json:"last_error"`
}

func (r *vpcMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_member"
}

func (r *vpcMemberResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Joins a rootserver to a VPC. A running server gets the network card hot-plugged. Import with `vpc_id/server_id`.",
		Attributes: map[string]schema.Attribute{
			"id":        schema.StringAttribute{Computed: true, MarkdownDescription: "`vpc_id/server_id`.", PlanModifiers: keep},
			"vpc_id":    schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"server_id": schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"private_ip": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Private address in the VPC. The lowest free address is used when not set. Changing it re-joins the server.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"allow_restart": schema.BoolAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Allow a restart of the server if the hypervisor refuses to hot-plug the network card. Default false.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"nic":             schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"guest_interface": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"status":          schema.StringAttribute{Computed: true, PlanModifiers: keep},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *vpcMemberResource) findMember(ctx context.Context, vpcID, serverID string) (*apiVPCMember, error) {
	var out struct {
		Members []apiVPCMember `json:"members"`
	}
	if err := r.client.Get(ctx, vpcPath+"/"+vpcID+"/members", &out); err != nil {
		return nil, err
	}
	for i := range out.Members {
		if out.Members[i].MemberType == "rootserver" && out.Members[i].MemberID == serverID {
			return &out.Members[i], nil
		}
	}
	return nil, nil
}

func (m *vpcMemberModel) apply(mem apiVPCMember) {
	m.PrivateIP = strOrNull(mem.PrivateIP)
	m.NIC = strOrNull(mem.NIC)
	m.GuestInterface = strOrNull(mem.GuestInterface)
	m.Status = types.StringValue(mem.Status)
	if m.AllowRestart.IsNull() || m.AllowRestart.IsUnknown() {
		m.AllowRestart = types.BoolValue(false)
	}
}

func (r *vpcMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan vpcMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"rootserver_id": plan.ServerID.ValueString(), "allow_restart": plan.AllowRestart.ValueBool()}
	if v := optString(plan.PrivateIP); v != nil {
		body["private_ip"] = *v
	}
	vpcID, serverID := plan.VPCID.ValueString(), plan.ServerID.ValueString()
	if err := runRootserverTask(ctx, r.client, "POST", vpcPath+"/"+vpcID+"/link", body, timeout); err != nil {
		addAPIError(&resp.Diagnostics, "Could not join the server to the VPC", err, fieldPaths{"private_ip": path.Root("private_ip"), "rootserver_id": path.Root("server_id")})
		return
	}
	mem, err := r.findMember(ctx, vpcID, serverID)
	if err != nil || mem == nil {
		if err == nil {
			err = fmt.Errorf("server %s is not listed as member of VPC %s after the join task completed", serverID, vpcID)
		}
		resp.Diagnostics.AddError("Could not read VPC member", err.Error())
		return
	}
	plan.ID = types.StringValue(vpcID + "/" + serverID)
	plan.apply(*mem)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *vpcMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state vpcMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mem, err := r.findMember(ctx, state.VPCID.ValueString(), state.ServerID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read VPC members", err, nil)
		return
	}
	if mem == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.ID = types.StringValue(state.VPCID.ValueString() + "/" + state.ServerID.ValueString())
	state.apply(*mem)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *vpcMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Only allow_restart can change in place; it is a flag for the next join or leave.
	var plan vpcMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *vpcMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state vpcMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"rootserver_id": state.ServerID.ValueString(), "allow_restart": state.AllowRestart.ValueBool()}
	if err := runRootserverTask(ctx, r.client, "POST", vpcPath+"/"+state.VPCID.ValueString()+"/unlink", body, timeout); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not remove the server from the VPC", err, nil)
	}
}

func (r *vpcMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := splitID(req.ID, 2, "vpc_id/server_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid import id", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("server_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
