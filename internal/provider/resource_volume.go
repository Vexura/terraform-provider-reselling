package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

var (
	_ resource.Resource                = (*volumeResource)(nil)
	_ resource.ResourceWithImportState = (*volumeResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*volumeResource)(nil)
)

type volumeResource struct{ base }

// NewVolumeResource manages a block storage volume.
func NewVolumeResource() resource.Resource { return &volumeResource{} }

type volumeModel struct {
	ID              types.String   `tfsdk:"id"`
	Name            types.String   `tfsdk:"name"`
	StorageClassID  types.String   `tfsdk:"storage_class_id"`
	SizeGB          types.Int64    `tfsdk:"size_gb"`
	Status          types.String   `tfsdk:"status"`
	ClusterID       types.String   `tfsdk:"cluster_id"`
	LocationID      types.String   `tfsdk:"location_id"`
	ServerID        types.String   `tfsdk:"server_id"`
	Device          types.String   `tfsdk:"device"`
	GuestDevicePath types.String   `tfsdk:"guest_device_path"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

type apiVolume struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	SizeGB          int64   `json:"size_gb"`
	PendingSizeGB   *int64  `json:"pending_size_gb"`
	Status          string  `json:"status"`
	StorageClass    *idRef  `json:"storage_class"`
	ClusterID       *string `json:"cluster_id"`
	Location        *idRef  `json:"location"`
	Server          *idRef  `json:"server"`
	Device          *string `json:"device"`
	GuestDevicePath *string `json:"guest_device_path"`
	ErrorMessage    *string `json:"error_message"`
}

const volumesPath = "/products/rootserver/volumes"

var volumeFields = fieldPaths{"name": path.Root("name"), "storage_class_id": path.Root("storage_class_id"), "size_gb": path.Root("size_gb")}

func (r *volumeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rootserver_volume"
}

func (r *volumeResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A block storage volume on a cluster. Attach it with `reselling_rootserver_volume_attachment`, so that the volume " +
			"survives when a server is replaced. A volume only grows, and only while it is attached.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name. Changed in place."},
			"storage_class_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Storage class (see data source `reselling_storage_classes`); it decides the cluster the volume lives on. Changing it replaces the volume.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"size_gb": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Size in GB. Grows in place while the volume is attached; a smaller value replaces the volume (all data is lost).",
				Validators:          []validator.Int64{int64validator.Between(1, 1048576)},
			},
			"status":            schema.StringAttribute{Computed: true},
			"cluster_id":        schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"location_id":       schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"server_id":         schema.StringAttribute{Computed: true, MarkdownDescription: "Server the volume is attached to, if any."},
			"device":            schema.StringAttribute{Computed: true},
			"guest_device_path": schema.StringAttribute{Computed: true, MarkdownDescription: "Stable path of the disk inside the guest."},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *volumeResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var planSize, stateSize types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("size_gb"), &planSize)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("size_gb"), &stateSize)...)
	if planSize.IsUnknown() || planSize.IsNull() || stateSize.IsNull() {
		return
	}
	if planSize.ValueInt64() < stateSize.ValueInt64() {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("size_gb"))
		resp.Diagnostics.AddAttributeWarning(path.Root("size_gb"), "Volume cannot shrink",
			fmt.Sprintf("A volume only grows (%d GB -> %d GB). Terraform will delete it and create a new, empty one.", stateSize.ValueInt64(), planSize.ValueInt64()))
	}
}

func (m *volumeModel) apply(v apiVolume) {
	m.ID = types.StringValue(v.ID)
	m.Name = types.StringValue(v.Name)
	size := v.SizeGB
	if v.PendingSizeGB != nil && *v.PendingSizeGB > size {
		size = *v.PendingSizeGB
	}
	m.SizeGB = types.Int64Value(size)
	if v.StorageClass != nil {
		m.StorageClassID = types.StringValue(v.StorageClass.ID)
	}
	m.Status = types.StringValue(v.Status)
	m.ClusterID = strOrNull(v.ClusterID)
	m.LocationID = refID(v.Location)
	m.ServerID = refID(v.Server)
	m.Device = strOrNull(v.Device)
	m.GuestDevicePath = strOrNull(v.GuestDevicePath)
}

func getVolume(ctx context.Context, c *client.Client, id string) (apiVolume, error) {
	var out struct {
		Volume apiVolume `json:"volume"`
	}
	err := c.Get(ctx, volumesPath+"/"+id, &out)
	return out.Volume, err
}

// waitVolume polls the volume until done says so; a volume in status error fails the wait.
func waitVolume(ctx context.Context, c *client.Client, id string, timeout time.Duration, what string, done func(apiVolume) bool) (apiVolume, error) {
	var last apiVolume
	err := waitFor(ctx, timeout, what, func(ctx context.Context) (bool, error) {
		v, err := getVolume(ctx, c, id)
		if err != nil {
			return false, err
		}
		last = v
		if v.Status == "error" {
			msg := "volume is in status error"
			if v.ErrorMessage != nil {
				msg += ": " + *v.ErrorMessage
			}
			return false, fmt.Errorf("%s", msg)
		}
		return done(v), nil
	})
	return last, err
}

func volumeSettled(v apiVolume) bool {
	return (v.Status == "available" || v.Status == "attached") && (v.PendingSizeGB == nil || *v.PendingSizeGB == 0)
}

func (r *volumeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Volume apiVolume `json:"volume"`
	}
	body := map[string]any{"name": plan.Name.ValueString(), "storage_class_id": plan.StorageClassID.ValueString(), "size_gb": plan.SizeGB.ValueInt64()}
	if err := r.client.Post(ctx, volumesPath, body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create volume", err, volumeFields)
		return
	}
	plan.apply(out.Volume)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

	v, err := waitVolume(ctx, r.client, out.Volume.ID, timeout, "volume to be created", volumeSettled)
	if err != nil {
		resp.Diagnostics.AddError("Volume creation did not finish", err.Error())
		return
	}
	plan.apply(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *volumeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := getVolume(ctx, r.client, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read volume", err, nil)
		return
	}
	if v.Status == "deleting" {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *volumeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state volumeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Update(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if !plan.Name.Equal(state.Name) {
		if err := r.client.Patch(ctx, volumesPath+"/"+id, map[string]any{"name": plan.Name.ValueString()}, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not rename volume", err, volumeFields)
			return
		}
	}
	if plan.SizeGB.ValueInt64() > state.SizeGB.ValueInt64() {
		err := retryBusy(ctx, timeout, func(ctx context.Context) error {
			return r.client.Post(ctx, volumesPath+"/"+id+"/resize", map[string]any{"size_gb": plan.SizeGB.ValueInt64()}, nil)
		})
		if err != nil {
			addAPIError(&resp.Diagnostics, "Could not grow volume", err, volumeFields)
			return
		}
		if _, err := waitVolume(ctx, r.client, id, timeout, "volume to be resized", func(v apiVolume) bool {
			return volumeSettled(v) && v.SizeGB >= plan.SizeGB.ValueInt64()
		}); err != nil {
			resp.Diagnostics.AddError("Volume resize did not finish", err.Error())
			return
		}
	}
	v, err := getVolume(ctx, r.client, id)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Could not read volume", err, nil)
		return
	}
	plan.apply(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *volumeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	err := retryBusy(ctx, timeout, func(ctx context.Context) error {
		return r.client.Delete(ctx, volumesPath+"/"+id, nil, nil)
	})
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		addAPIError(&resp.Diagnostics, "Could not delete volume", err, nil)
		return
	}
	err = waitFor(ctx, timeout, "volume to be deleted", func(ctx context.Context) (bool, error) {
		_, err := getVolume(ctx, r.client, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
	if err != nil {
		resp.Diagnostics.AddError("Volume deletion did not finish", err.Error())
	}
}

func (r *volumeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// --- attachment ---

var (
	_ resource.Resource                = (*volumeAttachmentResource)(nil)
	_ resource.ResourceWithImportState = (*volumeAttachmentResource)(nil)
)

type volumeAttachmentResource struct{ base }

// NewVolumeAttachmentResource attaches a volume to a rootserver.
func NewVolumeAttachmentResource() resource.Resource { return &volumeAttachmentResource{} }

type volumeAttachmentModel struct {
	ID              types.String   `tfsdk:"id"`
	VolumeID        types.String   `tfsdk:"volume_id"`
	ServerID        types.String   `tfsdk:"server_id"`
	Device          types.String   `tfsdk:"device"`
	GuestDevicePath types.String   `tfsdk:"guest_device_path"`
	Timeouts        timeouts.Value `tfsdk:"timeouts"`
}

func (r *volumeAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rootserver_volume_attachment"
}

func (r *volumeAttachmentResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Attaches a volume to a rootserver on the same node (next free SCSI slot, also while the server runs). " +
			"Destroying the attachment detaches the volume; the volume itself stays. Import with the volume id.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Computed: true, MarkdownDescription: "The volume id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"volume_id":         schema.StringAttribute{Required: true, PlanModifiers: replace},
			"server_id":         schema.StringAttribute{Required: true, PlanModifiers: replace},
			"device":            schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"guest_device_path": schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (m *volumeAttachmentModel) apply(v apiVolume) {
	m.ID = types.StringValue(v.ID)
	m.VolumeID = types.StringValue(v.ID)
	m.ServerID = refID(v.Server)
	m.Device = strOrNull(v.Device)
	m.GuestDevicePath = strOrNull(v.GuestDevicePath)
}

func (r *volumeAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan volumeAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	volumeID, serverID := plan.VolumeID.ValueString(), plan.ServerID.ValueString()
	err := retryBusy(ctx, timeout, func(ctx context.Context) error {
		return r.client.Post(ctx, volumesPath+"/"+volumeID+"/attach", map[string]any{"server_id": serverID}, nil)
	})
	if err != nil {
		addAPIError(&resp.Diagnostics, "Could not attach volume", err, fieldPaths{"server_id": path.Root("server_id")})
		return
	}
	v, err := waitVolume(ctx, r.client, volumeID, timeout, "volume to be attached", func(v apiVolume) bool {
		return v.Status == "attached" && v.Server != nil && v.Server.ID == serverID
	})
	if err != nil {
		resp.Diagnostics.AddError("Volume attachment did not finish", err.Error())
		return
	}
	plan.apply(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *volumeAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state volumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := getVolume(ctx, r.client, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read volume", err, nil)
		return
	}
	if v.Server == nil || (!state.ServerID.IsNull() && v.Server.ID != state.ServerID.ValueString()) {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *volumeAttachmentResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected update", "Every attribute of an attachment forces a new attachment.")
}

func (r *volumeAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state volumeAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 20*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.VolumeID.ValueString()
	err := retryBusy(ctx, timeout, func(ctx context.Context) error {
		return r.client.Post(ctx, volumesPath+"/"+id+"/detach", map[string]any{}, nil)
	})
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		addAPIError(&resp.Diagnostics, "Could not detach volume", err, nil)
		return
	}
	if _, err := waitVolume(ctx, r.client, id, timeout, "volume to be detached", func(v apiVolume) bool {
		return v.Server == nil && v.Status == "available"
	}); err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Volume detachment did not finish", err.Error())
	}
}

func (r *volumeAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("volume_id"), req.ID)...)
}
