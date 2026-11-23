package provider

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

var (
	_ resource.Resource                   = (*rootserverResource)(nil)
	_ resource.ResourceWithImportState    = (*rootserverResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*rootserverResource)(nil)
	_ resource.ResourceWithValidateConfig = (*rootserverResource)(nil)
)

type rootserverResource struct{ base }

// NewRootserverResource manages a rootserver (a virtual machine).
func NewRootserverResource() resource.Resource { return &rootserverResource{} }

type rootserverModel struct {
	ID               types.String   `tfsdk:"id"`
	ClusterID        types.String   `tfsdk:"cluster_id"`
	LocationID       types.String   `tfsdk:"location_id"`
	OSVersionID      types.String   `tfsdk:"os_version_id"`
	ImageID          types.String   `tfsdk:"image_id"`
	Hostname         types.String   `tfsdk:"hostname"`
	Cores            types.Int64    `tfsdk:"cores"`
	RAM              types.Int64    `tfsdk:"ram"`
	Disk             types.Int64    `tfsdk:"disk"`
	IPv4Count        types.Int64    `tfsdk:"ipv4_count"`
	IPv6Count        types.Int64    `tfsdk:"ipv6_count"`
	SSHKeyIDs        types.Set      `tfsdk:"ssh_key_ids"`
	StartupScriptID  types.String   `tfsdk:"startup_script_id"`
	UserData         types.String   `tfsdk:"user_data"`
	UserDataSHA256   types.String   `tfsdk:"user_data_sha256"`
	HasUserData      types.Bool     `tfsdk:"has_user_data"`
	Labels           types.List     `tfsdk:"labels"`
	Notes            types.String   `tfsdk:"notes"`
	DeleteProtection types.Bool     `tfsdk:"delete_protection"`
	BandwidthTierID  types.String   `tfsdk:"bandwidth_tier_id"`
	AdminPassword    types.String   `tfsdk:"admin_password"`
	AdminUser        types.String   `tfsdk:"admin_user"`
	Status           types.String   `tfsdk:"status"`
	PrimaryIP        types.String   `tfsdk:"primary_ip"`
	IPv4Addresses    types.List     `tfsdk:"ipv4_addresses"`
	IPv6Subnets      types.List     `tfsdk:"ipv6_subnets"`
	VolumeOnDelete   []volumeChoice `tfsdk:"volume_on_delete"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

type volumeChoice struct {
	VolumeID types.String `tfsdk:"volume_id"`
	Action   types.String `tfsdk:"action"`
}

type apiRootserver struct {
	ID               string   `json:"id"`
	HostName         string   `json:"host_name"`
	Labels           []string `json:"labels"`
	Notes            *string  `json:"notes"`
	DeleteProtection bool     `json:"delete_protection"`
	Status           string   `json:"status"`
	Cores            int64    `json:"cores"`
	RAM              int64    `json:"ram"`
	Disk             int64    `json:"disk"`
	PrimaryIP        *string  `json:"primary_ip"`
	LocationID       string   `json:"location_id"`
	ClusterID        string   `json:"cluster_id"`
	ErrorMessage     *string  `json:"error_message"`
	ErrorCode        *string  `json:"error_code"`
	HasUserData      bool     `json:"has_user_data"`
	AdminUser        *string  `json:"admin_user"`
	AdminPassword    *string  `json:"admin_password"`
	BandwidthTierID  *string  `json:"bandwidth_tier_id"`
	StartupScriptID  *string  `json:"startup_script_id"`
	ImageID          *string  `json:"image_id"`
	OS               *idRef   `json:"os"`
	Provisioning     *struct {
		Stage  string `json:"stage"`
		Status string `json:"status"`
	} `json:"provisioning"`
	IPv4Addresses []struct {
		ID      string `json:"id"`
		Address string `json:"address"`
		Type    string `json:"type"`
	} `json:"ipv4_addresses"`
	IPv6Subnets []struct {
		ID     string `json:"id"`
		Subnet string `json:"subnet"`
	} `json:"ipv6_subnets"`
	SSHKeys []idRef `json:"ssh_keys"`

	// presence of optional keys, so that an older API without them does not wipe state
	raw map[string]any
}

const rootserverPath = "/products/rootserver"

var rootserverFields = fieldPaths{
	"os_version_id": path.Root("os_version_id"), "image_id": path.Root("image_id"), "hostname": path.Root("hostname"),
	"cores": path.Root("cores"), "ram": path.Root("ram"), "disk": path.Root("disk"), "ipv4_count": path.Root("ipv4_count"),
	"ipv6_count": path.Root("ipv6_count"), "admin_password": path.Root("admin_password"), "ssh_key_ids": path.Root("ssh_key_ids"),
	"startup_script_id": path.Root("startup_script_id"), "bandwidth_tier_id": path.Root("bandwidth_tier_id"),
	"notes": path.Root("notes"), "labels": path.Root("labels"), "user_data": path.Root("user_data"), "password": path.Root("admin_password"),
	"volumes": path.Root("volume_on_delete"), "count": path.Root("ipv4_count"),
}

func (r *rootserverResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rootserver"
}

func (r *rootserverResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A rootserver (KVM virtual machine). Creation waits until the installation finished. " +
			"`cores`, `ram`, `disk` and `bandwidth_tier_id` are changed in place through a resize; the disk can only grow, a smaller " +
			"`disk` replaces the server. `hostname`, `labels`, `notes`, `ssh_key_ids`, `admin_password` and the IP counts are changed in place.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"cluster_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Hardware cluster the server runs on (see data source `reselling_locations`). Changing it replaces the server.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"location_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Location of the cluster.", PlanModifiers: keep},
			"os_version_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Operating system version to install (see data source `reselling_os_versions`). Exactly one of " +
					"`os_version_id` and `image_id` is required. Changing it replaces the server.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"image_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Custom image to install instead of an operating system (see `reselling_rootserver_image`). Changing it replaces the server.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"hostname": schema.StringAttribute{Required: true, MarkdownDescription: "Hostname or FQDN. Changed in place."},
			"cores": schema.Int64Attribute{
				Required: true, MarkdownDescription: "CPU cores. Changed in place (resize).",
				Validators: []validator.Int64{int64validator.AtLeast(1)},
			},
			"ram": schema.Int64Attribute{
				Required: true, MarkdownDescription: "Memory in MB (at least 512). Changed in place (resize).",
				Validators: []validator.Int64{int64validator.AtLeast(512)},
			},
			"disk": schema.Int64Attribute{
				Required: true, MarkdownDescription: "System disk in GB (at least 10). Grows in place; a smaller value replaces the server.",
				Validators: []validator.Int64{int64validator.AtLeast(10)},
			},
			"ipv4_count": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				MarkdownDescription: "Number of IPv4 addresses (0 for an IPv6-only Linux server). Default 1. More addresses are ordered in place, " +
					"fewer remove the newest secondary addresses.",
				Validators: []validator.Int64{int64validator.AtLeast(0)},
			},
			"ipv6_count": schema.Int64Attribute{
				Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				MarkdownDescription: "Number of IPv6 /64 subnets. Default 1. Changed in place.",
				Validators:          []validator.Int64{int64validator.AtLeast(0)},
			},
			"ssh_key_ids": schema.SetAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Ids of stored SSH keys (`reselling_ssh_key`). Keys are added to and removed from the running server in place.",
			},
			"startup_script_id": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Startup script that runs on installation. Changing it replaces the server.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"user_data": schema.StringAttribute{
				Optional: true, Sensitive: true, WriteOnly: true,
				MarkdownDescription: "cloud-init user data (`#cloud-config` or a script starting with `#!`, Linux only, max. 32768 bytes) that runs on the first boot. " +
					"Write-only: the content is never stored in the state, only its SHA-256 in `user_data_sha256`. A changed content replaces the server. " +
					"Requires Terraform 1.11 or later.",
			},
			"user_data_sha256": schema.StringAttribute{
				Computed: true, MarkdownDescription: "SHA-256 of the user data that was sent on creation.",
			},
			"has_user_data": schema.BoolAttribute{Computed: true, MarkdownDescription: "Whether the server has user data stored."},
			"labels": schema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Up to 10 labels (1-32 characters). Firewall groups and load balancer targets can select servers by label.",
				Validators:          []validator.List{listvalidator.SizeAtMost(10)},
			},
			"notes": schema.StringAttribute{Optional: true, MarkdownDescription: "Free-text note."},
			"delete_protection": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Protect the server from deletion. While it is on, `terraform destroy` fails for this server.",
			},
			"bandwidth_tier_id": schema.StringAttribute{
				Optional: true, Computed: true,
				MarkdownDescription: "Bandwidth tier of the cluster (see `reselling_locations`). Changed in place (resize).",
				PlanModifiers:       keep,
			},
			"admin_password": schema.StringAttribute{
				Optional: true, Computed: true, Sensitive: true,
				MarkdownDescription: "Root/Administrator password. Generated when not set. A changed value resets the password in place. " +
					"Read back only when the API key holds `rootservers:update`.",
				PlanModifiers: keep,
				Validators:    []validator.String{stringvalidator.LengthAtLeast(8)},
			},
			"admin_user":     schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"status":         schema.StringAttribute{Computed: true, MarkdownDescription: "Status, for example `running` or `stopped`."},
			"primary_ip":     schema.StringAttribute{Computed: true, MarkdownDescription: "Primary IPv4 address."},
			"ipv4_addresses": schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "All IPv4 addresses."},
			"ipv6_subnets":   schema.ListAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "All IPv6 subnets."},
		},
		Blocks: map[string]schema.Block{
			"volume_on_delete": schema.ListNestedBlock{
				MarkdownDescription: "What happens to an attached volume when the server is deleted. Volumes not listed are kept " +
					"(detached, they stay in the account and keep being billed).",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"volume_id": schema.StringAttribute{Required: true},
						"action": schema.StringAttribute{
							Required: true, MarkdownDescription: "`keep` or `delete`.",
							Validators: []validator.String{stringvalidator.OneOf("keep", "delete")},
						},
					},
				},
			},
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *rootserverResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var osVersion, image types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("os_version_id"), &osVersion)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("image_id"), &image)...)
	if osVersion.IsUnknown() || image.IsUnknown() {
		return
	}
	if osVersion.IsNull() == image.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("os_version_id"), "Choose what to install",
			"Set exactly one of os_version_id and image_id.")
	}
}

// ModifyPlan handles the write-only user_data (only its hash is planned) and the disk, which can only grow.
func (r *rootserverResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var userData types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("user_data"), &userData)...)
	if resp.Diagnostics.HasError() {
		return
	}

	newHash := types.StringNull()
	if userData.IsUnknown() {
		newHash = types.StringUnknown()
	} else if !userData.IsNull() && userData.ValueString() != "" {
		newHash = types.StringValue(sha256Hex(userData.ValueString()))
	}

	if req.State.Raw.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("user_data_sha256"), newHash)...)
		return
	}

	var state rootserverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	planned := state.UserDataSHA256
	switch {
	case newHash.IsUnknown():
		planned = newHash
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("user_data_sha256"))
	case newHash.IsNull():
		// Removing user_data from the configuration does not touch the server: the data only ran on its first boot.
		planned = types.StringNull()
	case state.UserDataSHA256.IsNull() && state.HasUserData.ValueBool():
		// Imported server that already has user data: adopt the configured content without replacing.
		planned = newHash
	case state.UserDataSHA256.ValueString() != newHash.ValueString():
		planned = newHash
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("user_data_sha256"))
		resp.Diagnostics.AddAttributeWarning(path.Root("user_data"), "User data changed",
			"user_data only runs on the first boot, so the server will be replaced to apply the new content.")
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("user_data_sha256"), planned)...)

	var disk types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("disk"), &disk)...)
	if !disk.IsUnknown() && !disk.IsNull() && !state.Disk.IsNull() && disk.ValueInt64() < state.Disk.ValueInt64() {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("disk"))
		resp.Diagnostics.AddAttributeWarning(path.Root("disk"), "Disk cannot shrink",
			fmt.Sprintf("The system disk of a rootserver can only grow (%d GB -> %d GB). Terraform will delete the server and create a new one; "+
				"all data on it is lost. Keep disk at %d or more to resize in place instead.", state.Disk.ValueInt64(), disk.ValueInt64(), state.Disk.ValueInt64()))
	}
}

func (r *rootserverResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rootserverModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var userData types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("user_data"), &userData)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := plan.Timeouts.Create(ctx, 60*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{
		"hostname":   plan.Hostname.ValueString(),
		"cores":      plan.Cores.ValueInt64(),
		"ram":        plan.RAM.ValueInt64(),
		"disk":       plan.Disk.ValueInt64(),
		"ipv4_count": plan.IPv4Count.ValueInt64(),
		"ipv6_count": plan.IPv6Count.ValueInt64(),
	}
	if v := optString(plan.OSVersionID); v != nil {
		body["os_version_id"] = *v
	}
	if v := optString(plan.ImageID); v != nil {
		body["image_id"] = *v
	}
	if keys := setStrings(ctx, plan.SSHKeyIDs, &resp.Diagnostics); len(keys) > 0 {
		body["ssh_key_ids"] = keys
	}
	for key, value := range map[string]types.String{
		"startup_script_id": plan.StartupScriptID, "bandwidth_tier_id": plan.BandwidthTierID,
		"notes": plan.Notes, "admin_password": plan.AdminPassword,
	} {
		if v := optString(value); v != nil {
			body[key] = *v
		}
	}
	if !userData.IsNull() && !userData.IsUnknown() && userData.ValueString() != "" {
		body["user_data"] = userData.ValueString()
	}

	var created struct {
		Server apiRootserver `json:"server"`
	}
	createPath := fmt.Sprintf("%s/cluster/%s/create", rootserverPath, plan.ClusterID.ValueString())
	if err := r.client.Post(ctx, createPath, body, &created, client.WithIdempotencyKey(newIdempotencyKey())); err != nil {
		addAPIError(&resp.Diagnostics, "Could not order rootserver", err, rootserverFields)
		return
	}

	labels := listStrings(ctx, plan.Labels, &resp.Diagnostics)
	plan.ID = types.StringValue(created.Server.ID)
	// Persist the id right away: if waiting fails, the server is tracked (and tainted) instead of leaked.
	r.fill(ctx, &plan, created.Server, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

	server, err := r.waitProvisioned(ctx, plan.ID.ValueString(), timeout)
	if err != nil {
		resp.Diagnostics.AddError("Rootserver installation did not finish", err.Error())
		return
	}

	// Labels are not part of the order: set them once the server exists.
	if len(labels) > 0 {
		if err := r.client.Patch(ctx, rootserverPath+"/"+plan.ID.ValueString(), map[string]any{"labels": labels}, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not set labels", err, rootserverFields)
			return
		}
		server.Labels = labels
		plan.Labels = stringList(labels)
	}

	if plan.DeleteProtection.ValueBool() {
		if err := r.client.Post(ctx, rootserverPath+"/"+plan.ID.ValueString()+"/delete-protection", map[string]any{"enabled": true}, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not enable delete protection", err, fieldPaths{"enabled": path.Root("delete_protection")})
			return
		}
		server.DeleteProtection = true
	}

	r.fill(ctx, &plan, server, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// waitProvisioned polls the server until the installation is over.
func (r *rootserverResource) waitProvisioned(ctx context.Context, id string, timeout time.Duration) (apiRootserver, error) {
	var server apiRootserver
	err := waitFor(ctx, timeout, "rootserver "+id+" to be installed", func(ctx context.Context) (bool, error) {
		s, err := r.get(ctx, id)
		if err != nil {
			return false, err
		}
		server = s
		switch {
		case s.Status == "error" || (s.Provisioning != nil && s.Provisioning.Status == "failed"):
			msg := "installation failed"
			if s.ErrorMessage != nil {
				msg = *s.ErrorMessage
			}
			code := ""
			if s.ErrorCode != nil {
				code = " (" + *s.ErrorCode + ")"
			}
			return false, fmt.Errorf("%s%s", msg, code)
		case s.Status == "pending" || s.Status == "installing" || s.Status == "provisioning" || s.Provisioning != nil:
			return false, nil
		}
		return true, nil
	})
	return server, err
}

func (r *rootserverResource) get(ctx context.Context, id string) (apiRootserver, error) {
	var raw map[string]any
	if err := r.client.Get(ctx, rootserverPath+"/"+id, &raw); err != nil {
		return apiRootserver{}, err
	}
	var s apiRootserver
	if err := remarshal(raw, &s); err != nil {
		return s, err
	}
	s.raw = raw
	return s, nil
}

// fill copies the API view of a server into the model.
func (r *rootserverResource) fill(ctx context.Context, m *rootserverModel, s apiRootserver, diags *diag.Diagnostics) {
	m.ID = types.StringValue(s.ID)
	if s.ClusterID != "" {
		m.ClusterID = types.StringValue(s.ClusterID)
	}
	m.LocationID = types.StringValue(s.LocationID)
	if s.HostName != "" {
		m.Hostname = types.StringValue(s.HostName)
	}
	if s.Cores > 0 {
		m.Cores = types.Int64Value(s.Cores)
		m.RAM = types.Int64Value(s.RAM)
		m.Disk = types.Int64Value(s.Disk)
	}
	m.Status = types.StringValue(s.Status)
	m.DeleteProtection = types.BoolValue(s.DeleteProtection)
	m.PrimaryIP = strOrNull(s.PrimaryIP)
	m.HasUserData = types.BoolValue(s.HasUserData)
	if m.UserDataSHA256.IsUnknown() {
		m.UserDataSHA256 = types.StringNull()
	}
	m.Labels = keepListIfSameSet(ctx, m.Labels, s.Labels)
	if s.Notes != nil && *s.Notes != "" {
		m.Notes = types.StringValue(*s.Notes)
	} else if !m.Notes.IsNull() && m.Notes.ValueString() != "" {
		m.Notes = types.StringNull()
	}
	if s.OS != nil && s.OS.ID != "" && (m.ImageID.IsNull() || m.OSVersionID.IsUnknown() || m.OSVersionID.IsNull()) {
		m.OSVersionID = types.StringValue(s.OS.ID)
	}
	if m.OSVersionID.IsUnknown() {
		m.OSVersionID = types.StringNull()
	}
	if _, present := s.raw["image_id"]; present {
		m.ImageID = strOrNull(s.ImageID)
	}
	if _, present := s.raw["startup_script_id"]; present {
		m.StartupScriptID = strOrNull(s.StartupScriptID)
	}
	if _, present := s.raw["bandwidth_tier_id"]; present {
		m.BandwidthTierID = strOrNull(s.BandwidthTierID)
	}
	if m.BandwidthTierID.IsUnknown() {
		m.BandwidthTierID = types.StringNull()
	}
	if s.AdminUser != nil {
		m.AdminUser = types.StringValue(*s.AdminUser)
	} else if m.AdminUser.IsUnknown() {
		m.AdminUser = types.StringNull()
	}
	if s.AdminPassword != nil && *s.AdminPassword != "" {
		m.AdminPassword = types.StringValue(*s.AdminPassword)
	} else if m.AdminPassword.IsUnknown() {
		m.AdminPassword = types.StringNull()
	}

	if s.raw != nil {
		v4 := make([]string, 0, len(s.IPv4Addresses))
		for _, ip := range s.IPv4Addresses {
			v4 = append(v4, ip.Address)
		}
		v6 := make([]string, 0, len(s.IPv6Subnets))
		for _, sn := range s.IPv6Subnets {
			v6 = append(v6, sn.Subnet)
		}
		m.IPv4Addresses = stringList(v4)
		m.IPv6Subnets = stringList(v6)
		// While the order is still being installed the addresses may not be assigned yet.
		if s.Status != "pending" && s.Status != "installing" {
			m.IPv4Count = types.Int64Value(int64(len(v4)))
			m.IPv6Count = types.Int64Value(int64(len(v6)))
		}
		keys := make([]string, 0, len(s.SSHKeys))
		for _, k := range s.SSHKeys {
			keys = append(keys, k.ID)
		}
		sort.Strings(keys)
		if len(keys) == 0 && m.SSHKeyIDs.IsNull() {
			// keep null
		} else {
			m.SSHKeyIDs = stringSet(keys)
		}
	} else {
		if m.IPv4Addresses.IsUnknown() {
			m.IPv4Addresses = types.ListNull(types.StringType)
		}
		if m.IPv6Subnets.IsUnknown() {
			m.IPv6Subnets = types.ListNull(types.StringType)
		}
	}
	_ = diags
}

func (r *rootserverResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state rootserverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	server, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read rootserver", err, nil)
		return
	}
	if server.Status == "deleted" || server.Status == "pending_deletion" {
		resp.State.RemoveResource(ctx)
		return
	}
	r.fill(ctx, &state, server, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *rootserverResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state rootserverModel
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
	base := rootserverPath + "/" + id

	// 1. Hostname, labels, notes
	meta := map[string]any{}
	if !plan.Hostname.Equal(state.Hostname) {
		meta["hostname"] = plan.Hostname.ValueString()
	}
	if !plan.Labels.Equal(state.Labels) {
		labels := listStrings(ctx, plan.Labels, &resp.Diagnostics)
		if labels == nil {
			labels = []string{}
		}
		meta["labels"] = labels
	}
	if !plan.Notes.Equal(state.Notes) {
		meta["notes"] = optString(plan.Notes)
	}
	if len(meta) > 0 {
		if err := r.client.Patch(ctx, base, meta, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not update rootserver", err, rootserverFields)
			return
		}
	}

	if !plan.DeleteProtection.Equal(state.DeleteProtection) {
		if err := r.client.Post(ctx, base+"/delete-protection", map[string]any{"enabled": plan.DeleteProtection.ValueBool()}, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not change delete protection", err, fieldPaths{"enabled": path.Root("delete_protection")})
			return
		}
	}

	// 2. SSH keys
	if !plan.SSHKeyIDs.Equal(state.SSHKeyIDs) {
		added, removed := diffStrings(setStrings(ctx, state.SSHKeyIDs, &resp.Diagnostics), setStrings(ctx, plan.SSHKeyIDs, &resp.Diagnostics))
		for _, key := range added {
			if err := r.client.Post(ctx, base+"/ssh-keys", map[string]any{"ssh_key_id": key}, nil); err != nil {
				addAPIError(&resp.Diagnostics, "Could not add SSH key "+key, err, fieldPaths{"ssh_key_id": path.Root("ssh_key_ids")})
				return
			}
		}
		for _, key := range removed {
			if err := r.client.Delete(ctx, base+"/ssh-keys/"+key, nil, nil); err != nil && !client.IsNotFound(err) {
				addAPIError(&resp.Diagnostics, "Could not remove SSH key "+key, err, nil)
				return
			}
		}
	}

	// 3. Resize
	resize := map[string]any{}
	if plan.Cores.ValueInt64() != state.Cores.ValueInt64() {
		resize["cores"] = plan.Cores.ValueInt64()
	}
	if plan.RAM.ValueInt64() != state.RAM.ValueInt64() {
		resize["ram"] = plan.RAM.ValueInt64()
	}
	if plan.Disk.ValueInt64() != state.Disk.ValueInt64() {
		resize["disk"] = plan.Disk.ValueInt64()
	}
	if !plan.BandwidthTierID.IsUnknown() && !plan.BandwidthTierID.Equal(state.BandwidthTierID) {
		resize["bandwidth_tier_id"] = optString(plan.BandwidthTierID)
	}
	if len(resize) > 0 {
		if err := r.runTask(ctx, base+"/resize", resize, timeout); err != nil {
			addAPIError(&resp.Diagnostics, "Could not resize rootserver", err, rootserverFields)
			return
		}
	}

	// 4. IP addresses
	if err := r.reconcileIPs(ctx, id, plan, state, timeout); err != nil {
		addAPIError(&resp.Diagnostics, "Could not change the IP addresses", err, rootserverFields)
		return
	}

	// 5. Root password
	if !plan.AdminPassword.IsUnknown() && !plan.AdminPassword.IsNull() && !plan.AdminPassword.Equal(state.AdminPassword) {
		if err := r.runTask(ctx, base+"/reset-root-password", map[string]any{"password": plan.AdminPassword.ValueString()}, timeout); err != nil {
			addAPIError(&resp.Diagnostics, "Could not reset the root password", err, rootserverFields)
			return
		}
	}

	server, err := r.get(ctx, id)
	if err != nil {
		addAPIError(&resp.Diagnostics, "Could not read rootserver", err, nil)
		return
	}
	r.fill(ctx, &plan, server, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// runTask sends an action that answers with a task and waits for the task. A busy server is waited for.
func (r *rootserverResource) runTask(ctx context.Context, path string, body any, timeout time.Duration) error {
	return runRootserverTask(ctx, r.client, http.MethodPost, path, body, timeout)
}

func runRootserverTask(ctx context.Context, c *client.Client, method, path string, body any, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var out struct {
		Task *task `json:"task"`
	}
	err := retryBusy(ctx, timeout, func(ctx context.Context) error {
		return c.Do(ctx, method, path, body, &out)
	})
	if err != nil {
		return err
	}
	if out.Task == nil {
		return nil
	}
	return waitForTask(ctx, c, out.Task.ID, time.Until(deadline))
}

func (r *rootserverResource) reconcileIPs(ctx context.Context, id string, plan, state rootserverModel, timeout time.Duration) error {
	base := rootserverPath + "/" + id
	if plan.IPv4Count.IsUnknown() || plan.IPv6Count.IsUnknown() {
		return nil
	}
	wantV4, haveV4 := plan.IPv4Count.ValueInt64(), state.IPv4Count.ValueInt64()
	wantV6, haveV6 := plan.IPv6Count.ValueInt64(), state.IPv6Count.ValueInt64()
	if wantV4 == haveV4 && wantV6 == haveV6 {
		return nil
	}
	if wantV4 > haveV4 {
		if err := r.runTask(ctx, base+"/ips/ipv4", map[string]any{"count": wantV4 - haveV4}, timeout); err != nil {
			return err
		}
	}
	if wantV6 > haveV6 {
		for i := haveV6; i < wantV6; i++ {
			if err := r.runTask(ctx, base+"/ips/ipv6", map[string]any{}, timeout); err != nil {
				return err
			}
		}
	}
	if wantV4 >= haveV4 && wantV6 >= haveV6 {
		return nil
	}
	server, err := r.get(ctx, id)
	if err != nil {
		return err
	}
	if wantV4 < haveV4 {
		removable := []string{}
		for _, ip := range server.IPv4Addresses {
			if ip.Type != "primary" {
				removable = append(removable, ip.ID)
			}
		}
		excess := int(int64(len(server.IPv4Addresses)) - wantV4)
		if excess > len(removable) {
			return fmt.Errorf("cannot go down to %d IPv4 addresses: the primary address cannot be removed", wantV4)
		}
		for i := 0; i < excess; i++ {
			ipID := removable[len(removable)-1-i]
			if err := runRootserverTask(ctx, r.client, http.MethodDelete, base+"/ips/ipv4/"+ipID, nil, timeout); err != nil {
				return err
			}
		}
	}
	if wantV6 < haveV6 {
		excess := len(server.IPv6Subnets) - int(wantV6)
		for i := 0; i < excess; i++ {
			subnet := server.IPv6Subnets[len(server.IPv6Subnets)-1-i]
			if err := runRootserverTask(ctx, r.client, http.MethodDelete, base+"/ips/ipv6/"+subnet.ID, nil, timeout); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *rootserverResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state rootserverModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	timeout, d := state.Timeouts.Delete(ctx, 30*time.Minute)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	choices := map[string]string{}
	for _, c := range state.VolumeOnDelete {
		if c.Action.ValueString() == "delete" {
			choices[c.VolumeID.ValueString()] = "delete"
		}
	}
	body := map[string]any{}
	if len(choices) > 0 {
		body["volumes"] = choices
	}
	id := state.ID.ValueString()
	deadline := time.Now().Add(timeout)
	if err := runRootserverTask(ctx, r.client, http.MethodPost, rootserverPath+"/"+id+"/delete", body, timeout); err != nil {
		if client.IsNotFound(err) {
			return
		}
		addAPIError(&resp.Diagnostics, "Could not delete rootserver", err, rootserverFields)
		return
	}
	err := waitFor(ctx, time.Until(deadline), "rootserver "+id+" to be deleted", func(ctx context.Context) (bool, error) {
		s, err := r.get(ctx, id)
		if client.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return s.Status == "deleted" || s.Status == "pending_deletion", nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Rootserver deletion did not finish", err.Error())
	}
}

func (r *rootserverResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
