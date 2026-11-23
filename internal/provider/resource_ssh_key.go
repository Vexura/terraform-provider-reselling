package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

var (
	_ resource.Resource                = (*sshKeyResource)(nil)
	_ resource.ResourceWithImportState = (*sshKeyResource)(nil)
)

type sshKeyResource struct{ base }

// NewSSHKeyResource manages a stored SSH key.
func NewSSHKeyResource() resource.Resource { return &sshKeyResource{} }

type sshKeyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	PublicKey   types.String `tfsdk:"public_key"`
	IsDefault   types.Bool   `tfsdk:"is_default"`
	Fingerprint types.String `tfsdk:"fingerprint"`
	KeyType     types.String `tfsdk:"key_type"`
}

type apiSSHKey struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"key_type"`
	IsDefault   bool   `json:"is_default"`
}

const sshKeysPath = "/products/rootserver/ssh-keys"

func (r *sshKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ssh_key"
}

func (r *sshKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A public SSH key stored in the account. Its id goes into `ssh_key_ids` of `reselling_rootserver`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name of the key."},
			"public_key": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Public key in OpenSSH format (`ssh-ed25519`, `ssh-rsa`, `ecdsa-sha2-nistp256/384/521`). Changing it replaces the key.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"is_default": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Mark the key as default key of the account. Only applied on creation.",
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.RequiresReplaceIfConfigured(), boolplanmodifier.UseStateForUnknown()},
			},
			"fingerprint": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SHA256 fingerprint.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"key_type": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

var sshKeyFields = fieldPaths{"name": path.Root("name"), "public_key": path.Root("public_key"), "is_default": path.Root("is_default")}

func (r *sshKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "public_key": plan.PublicKey.ValueString()}
	if v := optBool(plan.IsDefault); v != nil {
		body["is_default"] = *v
	}
	var out struct {
		SSHKey apiSSHKey `json:"ssh_key"`
	}
	if err := r.client.Post(ctx, sshKeysPath, body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create SSH key", err, sshKeyFields)
		return
	}
	plan.applyAPI(out.SSHKey, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (m *sshKeyModel) applyAPI(k apiSSHKey, keepKey bool) {
	m.ID = types.StringValue(k.ID)
	m.Name = types.StringValue(k.Name)
	// The API may drop a trailing comment or whitespace; keep the configured text when the key itself is the same.
	if !keepKey || m.PublicKey.IsNull() || m.PublicKey.IsUnknown() || keyBody(m.PublicKey.ValueString()) != keyBody(k.PublicKey) {
		m.PublicKey = types.StringValue(k.PublicKey)
	}
	m.IsDefault = types.BoolValue(k.IsDefault)
	m.Fingerprint = types.StringValue(k.Fingerprint)
	m.KeyType = types.StringValue(k.KeyType)
}

// keyBody is "type base64" without the comment.
func keyBody(key string) string {
	fields := strings.Fields(key)
	if len(fields) >= 2 {
		return fields[0] + " " + fields[1]
	}
	return strings.TrimSpace(key)
}

func (r *sshKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		SSHKey apiSSHKey `json:"ssh_key"`
	}
	if err := r.client.Get(ctx, sshKeysPath+"/"+state.ID.ValueString(), &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read SSH key", err, nil)
		return
	}
	state.applyAPI(out.SSHKey, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *sshKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan sshKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		SSHKey apiSSHKey `json:"ssh_key"`
	}
	if err := r.client.Patch(ctx, sshKeysPath+"/"+plan.ID.ValueString(), map[string]any{"name": plan.Name.ValueString()}, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not rename SSH key", err, sshKeyFields)
		return
	}
	plan.applyAPI(out.SSHKey, true)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *sshKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state sshKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Delete(ctx, sshKeysPath+"/"+state.ID.ValueString(), nil, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete SSH key", err, nil)
	}
}

func (r *sshKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
