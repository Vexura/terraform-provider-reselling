package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

const kubernetesPath = "/products/kubernetes"

// --- cluster ---

var (
	_ resource.Resource                = (*kubernetesClusterResource)(nil)
	_ resource.ResourceWithImportState = (*kubernetesClusterResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*kubernetesClusterResource)(nil)
)

type kubernetesClusterResource struct{ base }

// NewKubernetesClusterResource manages a Kubernetes cluster with its worker pools.
func NewKubernetesClusterResource() resource.Resource { return &kubernetesClusterResource{} }

type kubernetesClusterModel struct {
	ID                 types.String    `tfsdk:"id"`
	Name               types.String    `tfsdk:"name"`
	LocationID         types.String    `tfsdk:"location_id"`
	ClusterID          types.String    `tfsdk:"cluster_id"`
	VersionID          types.String    `tfsdk:"version_id"`
	ControlPlanePlanID types.String    `tfsdk:"control_plane_plan_id"`
	ControlPlaneCount  types.Int64     `tfsdk:"control_plane_count"`
	VPCID              types.String    `tfsdk:"vpc_id"`
	PodCIDR            types.String    `tfsdk:"pod_cidr"`
	ServiceCIDR        types.String    `tfsdk:"service_cidr"`
	AutoUpgrade        types.Bool      `tfsdk:"auto_upgrade"`
	MaintenanceDay     types.Int64     `tfsdk:"maintenance_day"`
	MaintenanceHour    types.Int64     `tfsdk:"maintenance_hour"`
	WorkerPools        []nodePoolModel `tfsdk:"worker_pool"`
	Status             types.String    `tfsdk:"status"`
	Endpoint           types.String    `tfsdk:"endpoint"`
	Version            types.String    `tfsdk:"version"`
	Timeouts           timeouts.Value  `tfsdk:"timeouts"`
}

type nodePoolModel struct {
	ID                      types.String `tfsdk:"id"`
	Name                    types.String `tfsdk:"name"`
	PlanID                  types.String `tfsdk:"plan_id"`
	InfrastructureClusterID types.String `tfsdk:"infrastructure_cluster_id"`
	MinNodes                types.Int64  `tfsdk:"min_nodes"`
	MaxNodes                types.Int64  `tfsdk:"max_nodes"`
	AutoScale               types.Bool   `tfsdk:"auto_scale"`
	LabelKey                types.String `tfsdk:"label_key"`
	LabelValue              types.String `tfsdk:"label_value"`
	NodeCount               types.Int64  `tfsdk:"node_count"`
}

type apiNodePool struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	IsControlPlane        bool    `json:"is_control_plane"`
	InfrastructureCluster *idRef  `json:"infrastructure_cluster"`
	Plan                  *idRef  `json:"plan"`
	MinNodes              int64   `json:"min_nodes"`
	MaxNodes              int64   `json:"max_nodes"`
	AutoScale             bool    `json:"auto_scale"`
	NodeCount             int64   `json:"node_count"`
	LabelKey              *string `json:"label_key"`
	LabelValue            *string `json:"label_value"`
}

type apiKubernetesCluster struct {
	ID                string          `json:"id"`
	Name              string          `json:"name"`
	Status            string          `json:"status"`
	ControlPlaneCount *int64          `json:"control_plane_count"`
	VPCID             *string         `json:"vpc_id"`
	PodCIDR           *string         `json:"pod_cidr"`
	ServiceCIDR       *string         `json:"service_cidr"`
	AutoUpgrade       bool            `json:"auto_upgrade"`
	Endpoint          *string         `json:"endpoint"`
	ErrorMessage      *string         `json:"error_message"`
	Location          json.RawMessage `json:"location"`
	Version           json.RawMessage `json:"version"`
	Maintenance       *struct {
		Day  *int64 `json:"day"`
		Hour *int64 `json:"hour"`
	} `json:"maintenance_window"`
	NodePools []apiNodePool `json:"node_pools"`
}

var kubernetesFields = fieldPaths{
	"name": path.Root("name"), "location_id": path.Root("location_id"), "cluster_id": path.Root("cluster_id"), "version_id": path.Root("version_id"),
	"control_plane_plan_id": path.Root("control_plane_plan_id"), "control_plane_count": path.Root("control_plane_count"), "vpc_id": path.Root("vpc_id"),
	"pod_cidr": path.Root("pod_cidr"), "service_cidr": path.Root("service_cidr"), "auto_upgrade": path.Root("auto_upgrade"),
	"maintenance_day": path.Root("maintenance_day"), "maintenance_hour": path.Root("maintenance_hour"), "worker_pools": path.Root("worker_pool"),
	"plan_id": path.Root("worker_pool"), "min_nodes": path.Root("worker_pool"), "max_nodes": path.Root("worker_pool"),
}

func (r *kubernetesClusterResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_cluster"
}

func (r *kubernetesClusterResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replaceIfSet := []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed Kubernetes cluster. Worker pools are `worker_pool` blocks, matched by name: new pools are added, " +
			"removed pools deleted and changed sizes or labels updated in place (a changed plan or CPU pool replaces that pool). " +
			"A new `version_id` starts an upgrade. Deleting marks the cluster for deletion with a 48 hour grace period.",
		Attributes: map[string]schema.Attribute{
			"id":                    schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"name":                  schema.StringAttribute{Required: true, MarkdownDescription: "Letters, digits and hyphens. Changed in place."},
			"location_id":           schema.StringAttribute{Required: true, PlanModifiers: replace},
			"cluster_id":            schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Hardware cluster (CPU pool) of the control plane."},
			"version_id":            schema.StringAttribute{Required: true, MarkdownDescription: "Kubernetes version (see `reselling_kubernetes_versions`). A newer version upgrades the cluster in place."},
			"control_plane_plan_id": schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Control plane plan (see `reselling_kubernetes_plans`)."},
			"control_plane_count": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				MarkdownDescription: "1 or 3 (highly available).",
				Validators:          []validator.Int64{int64validator.OneOf(1, 3)},
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"vpc_id":       schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: replaceIfSet, MarkdownDescription: "VPC of the nodes; one is created when not set."},
			"pod_cidr":     schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: replaceIfSet},
			"service_cidr": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: replaceIfSet},
			"auto_upgrade": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"maintenance_day": schema.Int64Attribute{
				Optional: true, Computed: true, MarkdownDescription: "0-6 (0 = Sunday).",
				Validators:    []validator.Int64{int64validator.Between(0, 6)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"maintenance_hour": schema.Int64Attribute{
				Optional: true, Computed: true, MarkdownDescription: "0-23.",
				Validators:    []validator.Int64{int64validator.Between(0, 23)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"status":   schema.StringAttribute{Computed: true},
			"endpoint": schema.StringAttribute{Computed: true, PlanModifiers: keep, MarkdownDescription: "API server endpoint."},
			"version":  schema.StringAttribute{Computed: true, MarkdownDescription: "Kubernetes version string."},
		},
		Blocks: map[string]schema.Block{
			"worker_pool": schema.ListNestedBlock{
				MarkdownDescription: "Worker node pool; at least one.",
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"id":                        schema.StringAttribute{Computed: true},
						"name":                      schema.StringAttribute{Required: true},
						"plan_id":                   schema.StringAttribute{Required: true, MarkdownDescription: "Worker plan."},
						"infrastructure_cluster_id": schema.StringAttribute{Required: true, MarkdownDescription: "Hardware cluster (CPU pool) of the nodes."},
						"min_nodes":                 schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 10)}},
						"max_nodes":                 schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 20)}},
						"auto_scale":                schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
						"label_key":                 schema.StringAttribute{Optional: true},
						"label_value":               schema.StringAttribute{Optional: true},
						"node_count":                schema.Int64Attribute{Computed: true},
					},
				},
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

// ModifyPlan keeps the ids of worker pools that stay (matched by name, same plan and CPU pool);
// new or replaced pools get a new id.
func (r *kubernetesClusterResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var planPools, statePools []nodePoolModel
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("worker_pool"), &planPools)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("worker_pool"), &statePools)...)
	if resp.Diagnostics.HasError() {
		return
	}
	byName := map[string]nodePoolModel{}
	for _, p := range statePools {
		byName[p.Name.ValueString()] = p
	}
	for i, p := range planPools {
		current, ok := byName[p.Name.ValueString()]
		if ok && current.PlanID.Equal(p.PlanID) && current.InfrastructureClusterID.Equal(p.InfrastructureClusterID) {
			planPools[i].ID = current.ID
		} else {
			planPools[i].ID = types.StringUnknown()
		}
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("worker_pool"), planPools)...)
}

func (r *kubernetesClusterResource) get(ctx context.Context, id string) (apiKubernetesCluster, error) {
	var out apiKubernetesCluster
	err := r.client.Get(ctx, kubernetesPath+"/"+id, &out)
	return out, err
}

// objectID reads the id of a nested object that is a plain string in some answers.
func objectID(raw json.RawMessage) (id, name string) {
	var obj struct {
		ID      string `json:"id"`
		Version string `json:"version"`
		Name    string `json:"name"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		if obj.Version != "" {
			return obj.ID, obj.Version
		}
		return obj.ID, obj.Name
	}
	var s string
	_ = json.Unmarshal(raw, &s)
	return "", s
}

func (m *kubernetesClusterModel) apply(c apiKubernetesCluster) {
	m.ID = types.StringValue(c.ID)
	m.Name = types.StringValue(c.Name)
	m.Status = types.StringValue(c.Status)
	m.Endpoint = strOrNull(c.Endpoint)
	if id, _ := objectID(c.Location); id != "" {
		m.LocationID = types.StringValue(id)
	}
	if id, version := objectID(c.Version); id != "" {
		m.VersionID = types.StringValue(id)
		m.Version = types.StringValue(version)
	} else if version != "" {
		m.Version = types.StringValue(version)
	}
	if m.Version.IsUnknown() {
		m.Version = types.StringNull()
	}
	if c.ControlPlaneCount != nil {
		m.ControlPlaneCount = types.Int64Value(*c.ControlPlaneCount)
	}
	m.VPCID = strOrNull(c.VPCID)
	m.PodCIDR = strOrNull(c.PodCIDR)
	m.ServiceCIDR = strOrNull(c.ServiceCIDR)
	m.AutoUpgrade = types.BoolValue(c.AutoUpgrade)
	if c.Maintenance != nil {
		m.MaintenanceDay = intOrNull(c.Maintenance.Day)
		m.MaintenanceHour = intOrNull(c.Maintenance.Hour)
	}
	if m.MaintenanceDay.IsUnknown() {
		m.MaintenanceDay = types.Int64Null()
	}
	if m.MaintenanceHour.IsUnknown() {
		m.MaintenanceHour = types.Int64Null()
	}
	if c.NodePools == nil {
		// The create and update answers carry no pools: keep the planned ones, with unknowns resolved.
		for i := range m.WorkerPools {
			if m.WorkerPools[i].ID.IsUnknown() {
				m.WorkerPools[i].ID = types.StringNull()
			}
			if m.WorkerPools[i].NodeCount.IsUnknown() {
				m.WorkerPools[i].NodeCount = types.Int64Null()
			}
		}
		return
	}

	// Keep the configured order of the pools; pools only the API knows are appended.
	byName := map[string]apiNodePool{}
	workers := []apiNodePool{}
	for _, p := range c.NodePools {
		if p.IsControlPlane {
			if p.Plan != nil {
				m.ControlPlanePlanID = types.StringValue(p.Plan.ID)
			}
			if p.InfrastructureCluster != nil {
				m.ClusterID = types.StringValue(p.InfrastructureCluster.ID)
			}
			continue
		}
		byName[p.Name] = p
		workers = append(workers, p)
	}
	pools := []nodePoolModel{}
	seen := map[string]bool{}
	for _, want := range m.WorkerPools {
		if p, ok := byName[want.Name.ValueString()]; ok {
			pools = append(pools, poolFromAPI(p))
			seen[p.Name] = true
		}
	}
	for _, p := range workers {
		if !seen[p.Name] {
			pools = append(pools, poolFromAPI(p))
		}
	}
	m.WorkerPools = pools
}

func poolFromAPI(p apiNodePool) nodePoolModel {
	return nodePoolModel{
		ID: types.StringValue(p.ID), Name: types.StringValue(p.Name), PlanID: refID(p.Plan),
		InfrastructureClusterID: refID(p.InfrastructureCluster), MinNodes: types.Int64Value(p.MinNodes),
		MaxNodes: types.Int64Value(p.MaxNodes), AutoScale: types.BoolValue(p.AutoScale),
		LabelKey: strEmptyNull(p.LabelKey), LabelValue: strEmptyNull(p.LabelValue), NodeCount: types.Int64Value(p.NodeCount),
	}
}

func (p nodePoolModel) body() map[string]any {
	return map[string]any{
		"name": p.Name.ValueString(), "plan_id": p.PlanID.ValueString(), "infrastructure_cluster_id": p.InfrastructureClusterID.ValueString(),
		"min_nodes": p.MinNodes.ValueInt64(), "max_nodes": p.MaxNodes.ValueInt64(), "auto_scale": p.AutoScale.ValueBool(),
		"label_key": optString(p.LabelKey), "label_value": optString(p.LabelValue),
	}
}

func (r *kubernetesClusterResource) waitRunning(ctx context.Context, id string, timeout time.Duration) (apiKubernetesCluster, error) {
	var c apiKubernetesCluster
	err := waitFor(ctx, timeout, "Kubernetes cluster "+id+" to run", func(ctx context.Context) (bool, error) {
		var err error
		c, err = r.get(ctx, id)
		if err != nil {
			return false, err
		}
		switch c.Status {
		case "running", "stopped", "locked":
			return true, nil
		case "error":
			msg := "cluster is in status error"
			if c.ErrorMessage != nil {
				msg += ": " + *c.ErrorMessage
			}
			return false, fmt.Errorf("%s", msg)
		case "pending_deletion", "deleting":
			return false, errGone
		}
		return false, nil
	})
	return c, err
}

func (r *kubernetesClusterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan kubernetesClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 60*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	pools := []map[string]any{}
	for _, p := range plan.WorkerPools {
		pools = append(pools, p.body())
	}
	body := map[string]any{
		"name": plan.Name.ValueString(), "location_id": plan.LocationID.ValueString(), "cluster_id": plan.ClusterID.ValueString(),
		"version_id": plan.VersionID.ValueString(), "control_plane_plan_id": plan.ControlPlanePlanID.ValueString(),
		"control_plane_count": plan.ControlPlaneCount.ValueInt64(), "auto_upgrade": plan.AutoUpgrade.ValueBool(), "worker_pools": pools,
	}
	for k, v := range map[string]types.String{"vpc_id": plan.VPCID, "pod_cidr": plan.PodCIDR, "service_cidr": plan.ServiceCIDR} {
		if p := optString(v); p != nil {
			body[k] = *p
		}
	}
	for k, v := range map[string]types.Int64{"maintenance_day": plan.MaintenanceDay, "maintenance_hour": plan.MaintenanceHour} {
		if p := optInt(v); p != nil {
			body[k] = *p
		}
	}
	var out struct {
		Cluster apiKubernetesCluster `json:"cluster"`
	}
	if err := r.client.Post(ctx, kubernetesPath+"/create", body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not order Kubernetes cluster", err, kubernetesFields)
		return
	}
	desired := plan
	plan.apply(out.Cluster)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

	c, err := r.waitRunning(ctx, out.Cluster.ID, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Kubernetes cluster provisioning did not finish", err.Error())
		return
	}
	desired.apply(c)
	resp.Diagnostics.Append(resp.State.Set(ctx, desired)...)
}

func (r *kubernetesClusterResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read Kubernetes cluster", err, nil)
		return
	}
	if c.Status == "pending_deletion" || c.Status == "deleting" {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(c)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *kubernetesClusterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state kubernetesClusterModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 60*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	base := kubernetesPath + "/" + id

	settings := map[string]any{}
	if !plan.Name.Equal(state.Name) {
		settings["name"] = plan.Name.ValueString()
	}
	if !plan.AutoUpgrade.Equal(state.AutoUpgrade) {
		settings["auto_upgrade"] = plan.AutoUpgrade.ValueBool()
	}
	if v := optInt(plan.MaintenanceDay); v != nil && !plan.MaintenanceDay.Equal(state.MaintenanceDay) {
		settings["maintenance_day"] = *v
	}
	if v := optInt(plan.MaintenanceHour); v != nil && !plan.MaintenanceHour.Equal(state.MaintenanceHour) {
		settings["maintenance_hour"] = *v
	}
	if len(settings) > 0 {
		if err := retryBusy(ctx, timeout, func(ctx context.Context) error { return r.client.Post(ctx, base+"/update", settings, nil) }); err != nil {
			addAPIError(&resp.Diagnostics, "Could not update Kubernetes cluster", err, kubernetesFields)
			return
		}
	}

	if !plan.VersionID.Equal(state.VersionID) {
		if err := retryBusy(ctx, timeout, func(ctx context.Context) error {
			return r.client.Post(ctx, base+"/upgrade", map[string]any{"version_id": plan.VersionID.ValueString()}, nil)
		}); err != nil {
			addAPIError(&resp.Diagnostics, "Could not upgrade Kubernetes cluster", err, kubernetesFields)
			return
		}
		if _, err := r.waitRunning(ctx, id, timeout); err != nil {
			resp.Diagnostics.AddError("Kubernetes upgrade did not finish", err.Error())
			return
		}
	}

	if err := r.reconcilePools(ctx, id, plan.WorkerPools, state.WorkerPools, timeout); err != nil {
		addAPIError(&resp.Diagnostics, "Could not change the worker pools", err, kubernetesFields)
		return
	}

	c, err := r.waitRunning(ctx, id, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Kubernetes cluster update did not finish", err.Error())
		return
	}
	plan.apply(c)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *kubernetesClusterResource) reconcilePools(ctx context.Context, clusterID string, want, have []nodePoolModel, timeout time.Duration) error {
	base := kubernetesPath + "/" + clusterID + "/node-pools"
	haveByName := map[string]nodePoolModel{}
	for _, p := range have {
		haveByName[p.Name.ValueString()] = p
	}
	wantByName := map[string]bool{}
	post := func(path string, body any) error {
		return retryBusy(ctx, timeout, func(ctx context.Context) error { return r.client.Post(ctx, path, body, nil) })
	}
	for _, p := range want {
		name := p.Name.ValueString()
		wantByName[name] = true
		current, exists := haveByName[name]
		if exists && (!current.PlanID.Equal(p.PlanID) || !current.InfrastructureClusterID.Equal(p.InfrastructureClusterID)) {
			// Plan and CPU pool of a pool are fixed: replace the pool.
			if err := post(base+"/"+current.ID.ValueString()+"/delete", map[string]any{}); err != nil {
				return err
			}
			if _, err := r.waitRunning(ctx, clusterID, timeout); err != nil {
				return err
			}
			exists = false
		}
		if !exists {
			if err := post(base+"/create", p.body()); err != nil {
				return err
			}
			if _, err := r.waitRunning(ctx, clusterID, timeout); err != nil {
				return err
			}
			continue
		}
		if current.MinNodes.Equal(p.MinNodes) && current.MaxNodes.Equal(p.MaxNodes) && current.AutoScale.Equal(p.AutoScale) &&
			current.LabelKey.Equal(p.LabelKey) && current.LabelValue.Equal(p.LabelValue) {
			continue
		}
		body := map[string]any{
			"min_nodes": p.MinNodes.ValueInt64(), "max_nodes": p.MaxNodes.ValueInt64(), "auto_scale": p.AutoScale.ValueBool(),
			"label_key": optString(p.LabelKey), "label_value": optString(p.LabelValue),
		}
		if err := post(base+"/"+current.ID.ValueString()+"/update", body); err != nil {
			return err
		}
	}
	for _, p := range have {
		if wantByName[p.Name.ValueString()] {
			continue
		}
		if err := post(base+"/"+p.ID.ValueString()+"/delete", map[string]any{}); err != nil && !client.IsNotFound(err) {
			return err
		}
		if _, err := r.waitRunning(ctx, clusterID, timeout); err != nil {
			return err
		}
	}
	return nil
}

func (r *kubernetesClusterResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state kubernetesClusterModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := retryBusy(ctx, 30*time.Minute, func(ctx context.Context) error {
		return r.client.Post(ctx, kubernetesPath+"/"+state.ID.ValueString()+"/delete", map[string]any{}, nil)
	})
	if err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete Kubernetes cluster", err, nil)
	}
}

func (r *kubernetesClusterResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// --- add-on ---

var (
	_ resource.Resource                = (*kubernetesAddonResource)(nil)
	_ resource.ResourceWithImportState = (*kubernetesAddonResource)(nil)
)

type kubernetesAddonResource struct{ base }

// NewKubernetesAddonResource manages an add-on booked on a Kubernetes cluster.
func NewKubernetesAddonResource() resource.Resource { return &kubernetesAddonResource{} }

type kubernetesAddonModel struct {
	ID              types.String   `tfsdk:"id"`
	ClusterID       types.String   `tfsdk:"cluster_id"`
	Addon           types.String   `tfsdk:"addon"`
	Version         types.String   `tfsdk:"version"`
	ValuesJSON      types.String   `tfsdk:"values_json"`
	ConfirmDataLoss types.Bool     `tfsdk:"confirm_data_loss"`
	Status          types.String   `tfsdk:"status"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

type apiAddon struct {
	ID           string         `json:"id"`
	Slug         string         `json:"slug"`
	Status       string         `json:"status"`
	Version      *string        `json:"version"`
	Values       map[string]any `json:"values"`
	ErrorMessage *string        `json:"error_message"`
}

func (r *kubernetesAddonResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_addon"
}

func (r *kubernetesAddonResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "An add-on booked on a running Kubernetes cluster (catalogue: `GET /products/kubernetes/addons`). Missing " +
			"dependencies are booked by the platform as well; they are not managed by this resource. Import with `cluster_id/installation_id`.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"cluster_id": schema.StringAttribute{Required: true, PlanModifiers: replace},
			"addon":      schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Slug of the add-on."},
			"version": schema.StringAttribute{
				Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
				MarkdownDescription: "Pinned version. Installed with the newest version; set it to move to another pinned version.",
			},
			"values_json": schema.StringAttribute{
				Optional: true, MarkdownDescription: "Values as JSON object (use `jsonencode`). Must match the values schema of the add-on.",
			},
			"confirm_data_loss": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Required to uninstall add-ons that own volumes or CRDs.",
			},
			"status": schema.StringAttribute{Computed: true},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *kubernetesAddonResource) list(ctx context.Context, clusterID string) ([]apiAddon, error) {
	var out struct {
		Addons []apiAddon `json:"addons"`
	}
	err := r.client.Get(ctx, kubernetesPath+"/"+clusterID+"/addons", &out)
	return out.Addons, err
}

func (r *kubernetesAddonResource) find(ctx context.Context, clusterID, id string) (*apiAddon, error) {
	addons, err := r.list(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	for i := range addons {
		if addons[i].ID == id {
			return &addons[i], nil
		}
	}
	return nil, nil
}

func (r *kubernetesAddonResource) waitInstalled(ctx context.Context, clusterID, id string, timeout time.Duration) (*apiAddon, error) {
	var found *apiAddon
	err := waitFor(ctx, timeout, "add-on to be installed", func(ctx context.Context) (bool, error) {
		a, err := r.find(ctx, clusterID, id)
		if err != nil {
			return false, err
		}
		if a == nil {
			return false, errGone
		}
		found = a
		switch a.Status {
		case "installed":
			return true, nil
		case "failed":
			msg := "add-on installation failed"
			if a.ErrorMessage != nil {
				msg += ": " + *a.ErrorMessage
			}
			return false, fmt.Errorf("%s", msg)
		}
		return false, nil
	})
	return found, err
}

func (m *kubernetesAddonModel) apply(a apiAddon) {
	m.ID = types.StringValue(a.ID)
	m.Addon = types.StringValue(a.Slug)
	m.Status = types.StringValue(a.Status)
	m.Version = strOrNull(a.Version)
	if len(a.Values) == 0 {
		if !m.ValuesJSON.IsNull() && jsonEqual(m.ValuesJSON.ValueString(), "{}") {
			return
		}
		m.ValuesJSON = types.StringNull()
		return
	}
	encoded, _ := json.Marshal(a.Values)
	if m.ValuesJSON.IsNull() || !jsonEqual(m.ValuesJSON.ValueString(), string(encoded)) {
		m.ValuesJSON = types.StringValue(string(encoded))
	}
}

func decodeValues(v types.String) (map[string]any, error) {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return map[string]any{}, nil
	}
	values := map[string]any{}
	if err := json.Unmarshal([]byte(v.ValueString()), &values); err != nil {
		return nil, fmt.Errorf("values_json is not a JSON object: %w", err)
	}
	return values, nil
}

func (r *kubernetesAddonResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan kubernetesAddonModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	values, err := decodeValues(plan.ValuesJSON)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("values_json"), "Invalid values", err.Error())
		return
	}
	clusterID := plan.ClusterID.ValueString()
	var out struct {
		Addons []apiAddon `json:"addons"`
	}
	fields := fieldPaths{"addon": path.Root("addon"), "values": path.Root("values_json"), "version": path.Root("version")}
	if err := r.client.Post(ctx, kubernetesPath+"/"+clusterID+"/addons/install", map[string]any{"addon": plan.Addon.ValueString(), "values": values}, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not install add-on", err, fields)
		return
	}
	var mine *apiAddon
	for i := range out.Addons {
		if out.Addons[i].Slug == plan.Addon.ValueString() {
			mine = &out.Addons[i]
		}
	}
	if mine == nil {
		resp.Diagnostics.AddError("Could not install add-on", "the API answer does not list the booked add-on "+plan.Addon.ValueString())
		return
	}
	desiredVersion := plan.Version
	plan.apply(*mine)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

	a, err := r.waitInstalled(ctx, clusterID, mine.ID, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Add-on installation did not finish", err.Error())
		return
	}
	if !desiredVersion.IsNull() && !desiredVersion.IsUnknown() && (a.Version == nil || *a.Version != desiredVersion.ValueString()) {
		if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/addons/%s/update", kubernetesPath, clusterID, mine.ID), map[string]any{"version": desiredVersion.ValueString()}, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not pin the add-on version", err, fields)
			return
		}
		if a, err = r.waitInstalled(ctx, clusterID, mine.ID, timeout); err != nil {
			resp.Diagnostics.AddError("Add-on update did not finish", err.Error())
			return
		}
	}
	plan.apply(*a)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *kubernetesAddonResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state kubernetesAddonModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.find(ctx, state.ClusterID.ValueString(), state.ID.ValueString())
	if err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not read add-ons", err, nil)
		return
	}
	if a == nil || a.Status == "removing" {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(*a)
	if state.ConfirmDataLoss.IsNull() {
		state.ConfirmDataLoss = types.BoolValue(false)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *kubernetesAddonResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state kubernetesAddonModel
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
	if !plan.Version.IsUnknown() && !plan.Version.Equal(state.Version) {
		body["version"] = plan.Version.ValueString()
	}
	if !plan.ValuesJSON.Equal(state.ValuesJSON) {
		values, err := decodeValues(plan.ValuesJSON)
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("values_json"), "Invalid values", err.Error())
			return
		}
		body["values"] = values
	}
	clusterID, id := state.ClusterID.ValueString(), state.ID.ValueString()
	if len(body) > 0 {
		if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/addons/%s/update", kubernetesPath, clusterID, id), body, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not update add-on", err, fieldPaths{"values": path.Root("values_json"), "version": path.Root("version")})
			return
		}
	}
	a, err := r.waitInstalled(ctx, clusterID, id, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Add-on update did not finish", err.Error())
		return
	}
	plan.apply(*a)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *kubernetesAddonResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state kubernetesAddonModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	clusterID, id := state.ClusterID.ValueString(), state.ID.ValueString()
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/addons/%s/uninstall", kubernetesPath, clusterID, id), map[string]any{"confirm_data_loss": state.ConfirmDataLoss.ValueBool()}, nil); err != nil {
		if client.IsNotFound(err) {
			return
		}
		addAPIError(&resp.Diagnostics, "Could not uninstall add-on", err, nil)
		return
	}
	err := waitFor(ctx, timeout, "add-on to be removed", func(ctx context.Context) (bool, error) {
		a, err := r.find(ctx, clusterID, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if a != nil && a.Status == "failed" {
			return false, fmt.Errorf("add-on removal failed")
		}
		return a == nil, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Add-on removal did not finish", err.Error())
	}
}

func (r *kubernetesAddonResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importChild(ctx, req, resp, "cluster_id", "cluster_id/installation_id")
}
