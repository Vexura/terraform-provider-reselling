package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

const imagesPath = "/products/rootserver/images"

// --- image ---

var (
	_ resource.Resource                = (*rootserverImageResource)(nil)
	_ resource.ResourceWithImportState = (*rootserverImageResource)(nil)
)

type rootserverImageResource struct{ base }

// NewRootserverImageResource captures the system disk of a rootserver into an image.
func NewRootserverImageResource() resource.Resource { return &rootserverImageResource{} }

type rootserverImageModel struct {
	ID                     types.String   `tfsdk:"id"`
	ServerID               types.String   `tfsdk:"server_id"`
	Name                   types.String   `tfsdk:"name"`
	Description            types.String   `tfsdk:"description"`
	Labels                 types.List     `tfsdk:"labels"`
	StopServer             types.Bool     `tfsdk:"stop_server"`
	Generalize             types.Bool     `tfsdk:"generalize"`
	SharedWithSubResellers types.Bool     `tfsdk:"shared_with_sub_resellers"`
	Status                 types.String   `tfsdk:"status"`
	OSVersionID            types.String   `tfsdk:"os_version_id"`
	ClusterID              types.String   `tfsdk:"cluster_id"`
	SizeGB                 types.Int64    `tfsdk:"size_gb"`
	MinDiskGB              types.Int64    `tfsdk:"min_disk_gb"`
	Timeouts               timeouts.Value `tfsdk:"timeouts"`
}

type apiImage struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	Description            *string  `json:"description"`
	Status                 string   `json:"status"`
	OS                     *idRef   `json:"os"`
	Location               *idRef   `json:"location"`
	Cluster                *idRef   `json:"cluster"`
	SizeGB                 *int64   `json:"size_gb"`
	MinDiskGB              *int64   `json:"min_disk_gb"`
	Generalized            bool     `json:"generalized"`
	SharedWithSubResellers bool     `json:"shared_with_sub_resellers"`
	Owned                  bool     `json:"owned"`
	Labels                 []string `json:"labels"`
	SourceServer           *idRef   `json:"source_server"`
	ErrorMessage           *string  `json:"error_message"`
}

var imageFields = fieldPaths{
	"server_id": path.Root("server_id"), "name": path.Root("name"), "description": path.Root("description"), "labels": path.Root("labels"),
	"stop_server": path.Root("stop_server"), "generalize": path.Root("generalize"), "shared_with_sub_resellers": path.Root("shared_with_sub_resellers"),
}

func (r *rootserverImageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rootserver_image"
}

func (r *rootserverImageResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replaceBool := []planmodifier.Bool{boolplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "An image captured from the system disk of a rootserver (volumes are not included). Servers are installed " +
			"from it with `image_id`. Billed per GB while it exists. Creation waits until the image is available.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"server_id":   schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Server to capture."},
			"name":        schema.StringAttribute{Required: true, MarkdownDescription: "Name. Changed in place."},
			"description": schema.StringAttribute{Optional: true, MarkdownDescription: "Changed in place."},
			"labels": schema.ListAttribute{
				Optional: true, ElementType: types.StringType, MarkdownDescription: "Up to 10 labels. Changed in place.",
				Validators: []validator.List{listvalidator.SizeAtMost(10)},
			},
			"stop_server": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: replaceBool,
				MarkdownDescription: "Shut the server down for a consistent image and start it again afterwards.",
			},
			"generalize": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false), PlanModifiers: replaceBool,
				MarkdownDescription: "Generalize the image (reset machine id, SSH host keys, cloud-init state).",
			},
			"shared_with_sub_resellers": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Offer the image to your sub-resellers (main resellers only).",
			},
			"status":        schema.StringAttribute{Computed: true},
			"os_version_id": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"cluster_id":    schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"size_gb":       schema.Int64Attribute{Computed: true},
			"min_disk_gb":   schema.Int64Attribute{Computed: true},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *rootserverImageResource) get(ctx context.Context, id string) (apiImage, error) {
	var out struct {
		Image apiImage `json:"image"`
	}
	err := r.client.Get(ctx, imagesPath+"/"+id, &out)
	return out.Image, err
}

func (m *rootserverImageModel) apply(ctx context.Context, img apiImage) {
	m.ID = types.StringValue(img.ID)
	m.Name = types.StringValue(img.Name)
	m.Description = strEmptyNull(img.Description)
	if !(len(img.Labels) == 0 && m.Labels.IsNull()) {
		m.Labels = keepListIfSameSet(ctx, m.Labels, img.Labels)
	}
	m.Status = types.StringValue(img.Status)
	m.OSVersionID = refID(img.OS)
	m.ClusterID = refID(img.Cluster)
	m.SizeGB = intOrNull(img.SizeGB)
	m.MinDiskGB = intOrNull(img.MinDiskGB)
	m.SharedWithSubResellers = types.BoolValue(img.SharedWithSubResellers)
	if img.SourceServer != nil && (m.ServerID.IsNull() || m.ServerID.IsUnknown()) {
		m.ServerID = types.StringValue(img.SourceServer.ID)
	}
	if m.StopServer.IsNull() {
		m.StopServer = types.BoolValue(false)
	}
	if m.Generalize.IsNull() || m.Generalize.IsUnknown() {
		m.Generalize = types.BoolValue(img.Generalized)
	}
}

func (r *rootserverImageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rootserverImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 2*time.Hour)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"server_id": plan.ServerID.ValueString(), "name": plan.Name.ValueString(),
		"stop_server": plan.StopServer.ValueBool(), "generalize": plan.Generalize.ValueBool(),
	}
	if v := optString(plan.Description); v != nil {
		body["description"] = *v
	}
	if labels := listStrings(ctx, plan.Labels, &resp.Diagnostics); labels != nil {
		body["labels"] = labels
	}
	var out struct {
		Image apiImage `json:"image"`
	}
	err := retryBusy(ctx, timeout, func(ctx context.Context) error { return r.client.Post(ctx, imagesPath, body, &out) })
	if err != nil {
		addAPIError(&resp.Diagnostics, "Could not capture image", err, imageFields)
		return
	}
	plan.apply(ctx, out.Image)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

	var img apiImage
	err = waitFor(ctx, timeout, "image to become available", func(ctx context.Context) (bool, error) {
		var err error
		img, err = r.get(ctx, out.Image.ID)
		if err != nil {
			return false, err
		}
		switch img.Status {
		case "available":
			return true, nil
		case "failed":
			msg := "image capture failed"
			if img.ErrorMessage != nil {
				msg += ": " + *img.ErrorMessage
			}
			return false, fmt.Errorf("%s", msg)
		}
		return false, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Image capture did not finish", err.Error())
		return
	}
	if plan.SharedWithSubResellers.ValueBool() {
		if err := r.client.Patch(ctx, imagesPath+"/"+img.ID, map[string]any{"shared_with_sub_resellers": true}, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not share image", err, imageFields)
			return
		}
		img.SharedWithSubResellers = true
	}
	plan.apply(ctx, img)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *rootserverImageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state rootserverImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	img, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read image", err, nil)
		return
	}
	if img.Status == "deleting" {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(ctx, img)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *rootserverImageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state rootserverImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "description": optString(plan.Description)}
	labels := listStrings(ctx, plan.Labels, &resp.Diagnostics)
	if labels == nil {
		labels = []string{}
	}
	body["labels"] = labels
	if !plan.SharedWithSubResellers.Equal(state.SharedWithSubResellers) {
		body["shared_with_sub_resellers"] = plan.SharedWithSubResellers.ValueBool()
	}
	var out struct {
		Image apiImage `json:"image"`
	}
	if err := r.client.Patch(ctx, imagesPath+"/"+state.ID.ValueString(), body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not update image", err, imageFields)
		return
	}
	plan.apply(ctx, out.Image)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *rootserverImageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rootserverImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := retryBusy(ctx, timeout, func(ctx context.Context) error {
		return r.client.Delete(ctx, imagesPath+"/"+state.ID.ValueString(), nil, nil)
	})
	if err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete image", err, nil)
	}
}

func (r *rootserverImageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// --- recipe ---

var (
	_ resource.Resource                = (*imageRecipeResource)(nil)
	_ resource.ResourceWithImportState = (*imageRecipeResource)(nil)
)

type imageRecipeResource struct{ base }

// NewImageRecipeResource manages an image builder recipe.
func NewImageRecipeResource() resource.Resource { return &imageRecipeResource{} }

type imageRecipeModel struct {
	ID          types.String      `tfsdk:"id"`
	Name        types.String      `tfsdk:"name"`
	Description types.String      `tfsdk:"description"`
	OSVersionID types.String      `tfsdk:"os_version_id"`
	Cores       types.Int64       `tfsdk:"cores"`
	RAM         types.Int64       `tfsdk:"ram"`
	Disk        types.Int64       `tfsdk:"disk"`
	Steps       []recipeStepModel `tfsdk:"step"`
}

type recipeStepModel struct {
	Type            types.String `tfsdk:"type"`
	StartupScriptID types.String `tfsdk:"startup_script_id"`
	Content         types.String `tfsdk:"content"`
}

type apiRecipe struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	OSVersionID string  `json:"os_version_id"`
	Cores       int64   `json:"cores"`
	RAM         int64   `json:"ram"`
	Disk        int64   `json:"disk"`
	Steps       []struct {
		Type            string  `json:"type"`
		StartupScriptID *string `json:"startup_script_id"`
		Content         *string `json:"content"`
	} `json:"steps"`
}

func (r *imageRecipeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image_recipe"
}

func (r *imageRecipeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An image builder recipe: a Linux base system, ordered steps and the size of the temporary build VM. " +
			"Builds are started through the API or the panel; they are not managed by Terraform.",
		Attributes: map[string]schema.Attribute{
			"id":            schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":          schema.StringAttribute{Required: true},
			"description":   schema.StringAttribute{Optional: true},
			"os_version_id": schema.StringAttribute{Required: true, MarkdownDescription: "Linux base system of the catalogue."},
			"cores":         schema.Int64Attribute{Required: true, Validators: []validator.Int64{int64validator.Between(1, 32)}},
			"ram":           schema.Int64Attribute{Required: true, MarkdownDescription: "MB (512-65536).", Validators: []validator.Int64{int64validator.Between(512, 65536)}},
			"disk":          schema.Int64Attribute{Required: true, MarkdownDescription: "GB (10-1000).", Validators: []validator.Int64{int64validator.Between(10, 1000)}},
		},
		Blocks: map[string]schema.Block{
			"step": schema.ListNestedBlock{
				MarkdownDescription: "Ordered build steps; at least one.",
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							Required: true, MarkdownDescription: "`startup_script` or `user_data`.",
							Validators: []validator.String{stringvalidator.OneOf("startup_script", "user_data")},
						},
						"startup_script_id": schema.StringAttribute{Optional: true, MarkdownDescription: "For type `startup_script`."},
						"content":           schema.StringAttribute{Optional: true, MarkdownDescription: "cloud-init document for type `user_data`."},
					},
				},
			},
		},
	}
}

func (m imageRecipeModel) body() map[string]any {
	steps := []map[string]any{}
	for _, s := range m.Steps {
		step := map[string]any{"type": s.Type.ValueString()}
		if v := optString(s.StartupScriptID); v != nil {
			step["startup_script_id"] = *v
		}
		if v := optString(s.Content); v != nil {
			step["content"] = *v
		}
		steps = append(steps, step)
	}
	return map[string]any{
		"name": m.Name.ValueString(), "description": optString(m.Description), "os_version_id": m.OSVersionID.ValueString(),
		"cores": m.Cores.ValueInt64(), "ram": m.RAM.ValueInt64(), "disk": m.Disk.ValueInt64(), "steps": steps,
	}
}

func (m *imageRecipeModel) apply(rc apiRecipe) {
	m.ID = types.StringValue(rc.ID)
	m.Name = types.StringValue(rc.Name)
	m.Description = strEmptyNull(rc.Description)
	m.OSVersionID = types.StringValue(rc.OSVersionID)
	m.Cores = types.Int64Value(rc.Cores)
	m.RAM = types.Int64Value(rc.RAM)
	m.Disk = types.Int64Value(rc.Disk)
	steps := make([]recipeStepModel, 0, len(rc.Steps))
	for _, s := range rc.Steps {
		steps = append(steps, recipeStepModel{Type: types.StringValue(s.Type), StartupScriptID: strEmptyNull(s.StartupScriptID), Content: strEmptyNull(s.Content)})
	}
	m.Steps = steps
}

var recipeFields = fieldPaths{
	"name": path.Root("name"), "description": path.Root("description"), "os_version_id": path.Root("os_version_id"),
	"cores": path.Root("cores"), "ram": path.Root("ram"), "disk": path.Root("disk"), "steps": path.Root("step"),
}

func (r *imageRecipeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan imageRecipeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Recipe apiRecipe `json:"recipe"`
	}
	if err := r.client.Post(ctx, imagesPath+"/recipes", plan.body(), &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create image recipe", err, recipeFields)
		return
	}
	plan.apply(out.Recipe)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *imageRecipeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state imageRecipeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Recipe apiRecipe `json:"recipe"`
	}
	if err := r.client.Get(ctx, imagesPath+"/recipes/"+state.ID.ValueString(), &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read image recipe", err, nil)
		return
	}
	state.apply(out.Recipe)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *imageRecipeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan imageRecipeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Recipe apiRecipe `json:"recipe"`
	}
	if err := r.client.Put(ctx, imagesPath+"/recipes/"+plan.ID.ValueString(), plan.body(), &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not update image recipe", err, recipeFields)
		return
	}
	plan.apply(out.Recipe)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *imageRecipeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state imageRecipeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, imagesPath+"/recipes/"+state.ID.ValueString(), nil, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete image recipe", err, nil)
	}
}

func (r *imageRecipeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
