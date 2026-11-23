package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

var (
	_ resource.Resource                = (*startupScriptResource)(nil)
	_ resource.ResourceWithImportState = (*startupScriptResource)(nil)
)

type startupScriptResource struct{ base }

// NewStartupScriptResource manages a stored startup script.
func NewStartupScriptResource() resource.Resource { return &startupScriptResource{} }

type startupScriptModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Type      types.String `tfsdk:"type"`
	Script    types.String `tfsdk:"script"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

type apiStartupScript struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Script    string `json:"script"`
	IsDefault bool   `json:"is_default"`
}

const startupScriptsPath = "/products/rootserver/startup-scripts"

func (r *startupScriptResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_startup_script"
}

func (r *startupScriptResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A startup script stored in the account. It runs when a server is installed with `startup_script_id`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name (max. 255 characters)."},
			"type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`bash` or `cloud_init`.",
				Validators:          []validator.String{stringvalidator.OneOf("bash", "cloud_init")},
			},
			"script": schema.StringAttribute{Required: true, MarkdownDescription: "Content of the script (max. 65000 characters)."},
			"is_default": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Make this the default script of the account. The first script of an account becomes the default " +
					"automatically. The flag can only be moved to another script, not removed.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

var startupScriptFields = fieldPaths{"name": path.Root("name"), "type": path.Root("type"), "script": path.Root("script"), "is_default": path.Root("is_default")}

func (m startupScriptModel) body() map[string]any {
	body := map[string]any{"name": m.Name.ValueString(), "type": m.Type.ValueString(), "script": m.Script.ValueString()}
	if v := optBool(m.IsDefault); v != nil && *v {
		body["is_default"] = true
	}
	return body
}

func (m *startupScriptModel) applyAPI(s apiStartupScript) {
	m.ID = types.StringValue(s.ID)
	m.Name = types.StringValue(s.Name)
	m.Type = types.StringValue(s.Type)
	m.Script = types.StringValue(s.Script)
	m.IsDefault = types.BoolValue(s.IsDefault)
}

func (r *startupScriptResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan startupScriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Script apiStartupScript `json:"startup_script"`
	}
	if err := r.client.Post(ctx, startupScriptsPath, plan.body(), &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create startup script", err, startupScriptFields)
		return
	}
	plan.applyAPI(out.Script)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *startupScriptResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state startupScriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Script apiStartupScript `json:"startup_script"`
	}
	if err := r.client.Get(ctx, startupScriptsPath+"/"+state.ID.ValueString(), &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read startup script", err, nil)
		return
	}
	state.applyAPI(out.Script)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *startupScriptResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state startupScriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.IsDefault.ValueBool() && !plan.IsDefault.IsNull() && !plan.IsDefault.ValueBool() {
		resp.Diagnostics.AddAttributeWarning(path.Root("is_default"), "Default flag stays",
			"The API cannot take the default flag away from a script. Set is_default = true on another script to move it.")
	}
	var out struct {
		Script apiStartupScript `json:"startup_script"`
	}
	if err := r.client.Patch(ctx, startupScriptsPath+"/"+state.ID.ValueString(), plan.body(), &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not update startup script", err, startupScriptFields)
		return
	}
	plan.applyAPI(out.Script)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *startupScriptResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state startupScriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, startupScriptsPath+"/"+state.ID.ValueString(), nil, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete startup script", err, nil)
	}
}

func (r *startupScriptResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
