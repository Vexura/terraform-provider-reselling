package provider

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

const databasePath = "/products/database"

// --- database server ---

var (
	_ resource.Resource                = (*databaseResource)(nil)
	_ resource.ResourceWithImportState = (*databaseResource)(nil)
)

type databaseResource struct{ base }

// NewDatabaseResource manages a managed database server.
func NewDatabaseResource() resource.Resource { return &databaseResource{} }

type databaseModel struct {
	ID               types.String   `tfsdk:"id"`
	Name             types.String   `tfsdk:"name"`
	Engine           types.String   `tfsdk:"engine"`
	Version          types.String   `tfsdk:"version"`
	LocationID       types.String   `tfsdk:"location_id"`
	VPCID            types.String   `tfsdk:"vpc_id"`
	PlanID           types.String   `tfsdk:"plan_id"`
	StorageClassID   types.String   `tfsdk:"storage_class_id"`
	TrustedSources   types.List     `tfsdk:"trusted_sources"`
	PublicAccess     types.Bool     `tfsdk:"public_access"`
	PublicSources    types.List     `tfsdk:"public_sources"`
	PublicAllowAny   types.Bool     `tfsdk:"public_allow_any"`
	PublicIPv6       types.Bool     `tfsdk:"public_ipv6"`
	MaintenanceDay   types.Int64    `tfsdk:"maintenance_day"`
	MaintenanceHour  types.Int64    `tfsdk:"maintenance_hour"`
	AutoMinorUpdates types.Bool     `tfsdk:"auto_minor_updates"`
	Status           types.String   `tfsdk:"status"`
	Host             types.String   `tfsdk:"host"`
	Port             types.Int64    `tfsdk:"port"`
	PublicAddresses  types.List     `tfsdk:"public_addresses"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

type apiDatabase struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Engine            string  `json:"engine"`
	Version           string  `json:"version"`
	Status            string  `json:"status"`
	Host              *string `json:"host"`
	Port              *int64  `json:"port"`
	Location          *idRef  `json:"location"`
	Plan              *idRef  `json:"plan"`
	VPC               *idRef  `json:"vpc"`
	StorageClass      *idRef  `json:"storage_class"`
	DeletionRequested bool    `json:"deletion_requested"`
	ErrorMessage      *string `json:"error_message"`
	PublicAccess      *struct {
		Enabled   bool     `json:"enabled"`
		IPv6      bool     `json:"ipv6"`
		Sources   []string `json:"sources"`
		AllowAny  bool     `json:"allow_any"`
		Addresses any      `json:"addresses"`
	} `json:"public_access"`
	TrustedSources []string `json:"trusted_sources"`
	Maintenance    *struct {
		Day              *int64 `json:"day"`
		Hour             *int64 `json:"hour"`
		AutoMinorUpdates bool   `json:"auto_minor_updates"`
	} `json:"maintenance"`
}

var databaseFields = fieldPaths{
	"name": path.Root("name"), "engine": path.Root("engine"), "version": path.Root("version"), "location_id": path.Root("location_id"),
	"vpc_id": path.Root("vpc_id"), "plan_id": path.Root("plan_id"), "storage_class_id": path.Root("storage_class_id"),
	"trusted_sources": path.Root("trusted_sources"), "public_access": path.Root("public_access"), "public_sources": path.Root("public_sources"),
	"public_allow_any": path.Root("public_allow_any"), "public_ipv6": path.Root("public_ipv6"), "maintenance_day": path.Root("maintenance_day"),
	"maintenance_hour": path.Root("maintenance_hour"), "auto_minor_updates": path.Root("auto_minor_updates"),
}

func (r *databaseResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database"
}

func (r *databaseResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A managed PostgreSQL or MariaDB server in a VPC. TLS is always required. Logical databases and users are " +
			"`reselling_database_db` and `reselling_database_user`. Deleting writes a final backup and moves the server to the trash for 48 hours.",
		Attributes: map[string]schema.Attribute{
			"id":   schema.StringAttribute{Computed: true, PlanModifiers: keep},
			"name": schema.StringAttribute{Required: true, MarkdownDescription: "Name. Changed in place."},
			"engine": schema.StringAttribute{
				Required: true, PlanModifiers: replace, MarkdownDescription: "`postgresql` or `mariadb`.",
				Validators: []validator.String{stringvalidator.OneOf("postgresql", "mariadb")},
			},
			"version":     schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Major version, for example `17` (see `reselling_database_plans`). Major upgrades are not offered."},
			"location_id": schema.StringAttribute{Required: true, PlanModifiers: replace},
			"vpc_id":      schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "VPC the server is reachable in."},
			"plan_id":     schema.StringAttribute{Required: true, MarkdownDescription: "Plan. Changed in place; the disk only grows."},
			"storage_class_id": schema.StringAttribute{
				Optional: true, Computed: true, MarkdownDescription: "Storage class; the platform picks one when not set.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured(), stringplanmodifier.UseStateForUnknown()},
			},
			"trusted_sources": schema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "CIDRs inside the VPC that may connect (max. 20). The whole VPC may connect when not set.",
			},
			"public_access": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Open the engine port on a public address (billed). Requires `public_sources` or `public_allow_any`.",
			},
			"public_sources": schema.ListAttribute{
				Optional: true, ElementType: types.StringType,
				MarkdownDescription: "Public CIDRs allowed to connect when `public_access` is on (max. 20).",
			},
			"public_allow_any": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Required to allow 0.0.0.0/0 or ::/0 in `public_sources`.",
			},
			"public_ipv6": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				MarkdownDescription: "Also open the port on a public IPv6 address.",
			},
			"maintenance_day": schema.Int64Attribute{
				Optional: true, Computed: true, MarkdownDescription: "1 = Monday ... 7 = Sunday.",
				Validators:    []validator.Int64{int64validator.Between(1, 7)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"maintenance_hour": schema.Int64Attribute{
				Optional: true, Computed: true, MarkdownDescription: "0-23, Europe/Berlin.",
				Validators:    []validator.Int64{int64validator.Between(0, 23)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"auto_minor_updates": schema.BoolAttribute{
				Optional: true, Computed: true, MarkdownDescription: "Install minor versions in the maintenance window.",
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{Computed: true},
			"host":   schema.StringAttribute{Computed: true, MarkdownDescription: "Private address in the VPC.", PlanModifiers: keep},
			"port": schema.Int64Attribute{
				Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"public_addresses": schema.ListAttribute{
				Computed: true, ElementType: types.StringType, MarkdownDescription: "Public addresses when `public_access` is on.",
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

func (r *databaseResource) get(ctx context.Context, id string) (apiDatabase, error) {
	var out apiDatabase
	err := r.client.Get(ctx, databasePath+"/"+id, &out)
	return out, err
}

func (m *databaseModel) apply(ctx context.Context, d apiDatabase) {
	m.ID = types.StringValue(d.ID)
	m.Name = types.StringValue(d.Name)
	m.Engine = types.StringValue(d.Engine)
	m.Version = types.StringValue(d.Version)
	if d.Location != nil {
		m.LocationID = types.StringValue(d.Location.ID)
	}
	if d.VPC != nil {
		m.VPCID = types.StringValue(d.VPC.ID)
	}
	if d.Plan != nil {
		m.PlanID = types.StringValue(d.Plan.ID)
	}
	m.StorageClassID = refID(d.StorageClass)
	m.Status = types.StringValue(d.Status)
	m.Host = strOrNull(d.Host)
	m.Port = intOrNull(d.Port)
	if d.TrustedSources != nil {
		if len(d.TrustedSources) == 0 && m.TrustedSources.IsNull() {
			// keep null
		} else {
			m.TrustedSources = keepListIfSameSet(ctx, m.TrustedSources, d.TrustedSources)
		}
	}
	addresses := []string{}
	if d.PublicAccess != nil {
		m.PublicAccess = types.BoolValue(d.PublicAccess.Enabled)
		m.PublicIPv6 = types.BoolValue(d.PublicAccess.IPv6)
		m.PublicAllowAny = types.BoolValue(d.PublicAccess.AllowAny)
		if !(len(d.PublicAccess.Sources) == 0 && m.PublicSources.IsNull()) {
			m.PublicSources = keepListIfSameSet(ctx, m.PublicSources, d.PublicAccess.Sources)
		}
		addresses = flattenAddresses(d.PublicAccess.Addresses)
	}
	m.PublicAddresses = stringList(addresses)
	if d.Maintenance != nil {
		m.MaintenanceDay = intOrNull(d.Maintenance.Day)
		m.MaintenanceHour = intOrNull(d.Maintenance.Hour)
		m.AutoMinorUpdates = types.BoolValue(d.Maintenance.AutoMinorUpdates)
	}
	for _, v := range []*types.Int64{&m.MaintenanceDay, &m.MaintenanceHour} {
		if v.IsUnknown() {
			*v = types.Int64Null()
		}
	}
	if m.AutoMinorUpdates.IsUnknown() {
		m.AutoMinorUpdates = types.BoolNull()
	}
	if m.PublicIPv6.IsUnknown() {
		m.PublicIPv6 = types.BoolValue(false)
	}
	if m.StorageClassID.IsUnknown() {
		m.StorageClassID = types.StringNull()
	}
}

// flattenAddresses accepts a list of strings, a list of {address|ip} objects or a map of family -> address.
func flattenAddresses(v any) []string {
	out := []string{}
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			switch i := item.(type) {
			case string:
				out = append(out, i)
			case map[string]any:
				for _, key := range []string{"address", "ip"} {
					if s, ok := i[key].(string); ok && s != "" {
						out = append(out, s)
						break
					}
				}
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if s, ok := t[k].(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func (r *databaseResource) waitRunning(ctx context.Context, id string, timeout time.Duration) (apiDatabase, error) {
	var db apiDatabase
	err := waitFor(ctx, timeout, "database "+id+" to run", func(ctx context.Context) (bool, error) {
		var err error
		db, err = r.get(ctx, id)
		if err != nil {
			return false, err
		}
		switch db.Status {
		case "running", "stopped":
			return true, nil
		case "error":
			msg := "database is in status error"
			if db.ErrorMessage != nil {
				msg += ": " + *db.ErrorMessage
			}
			return false, fmt.Errorf("%s", msg)
		}
		if managedGone(db.Status) {
			return false, errGone
		}
		return false, nil
	})
	return db, err
}

// settingsBody collects the fields of the update endpoint that differ from the current state.
func (r *databaseResource) settingsBody(ctx context.Context, plan databaseModel, current *databaseModel) map[string]any {
	body := map[string]any{}
	var d diag.Diagnostics
	if current == nil || !plan.TrustedSources.Equal(current.TrustedSources) {
		if current != nil || !plan.TrustedSources.IsNull() {
			body["trusted_sources"] = listStrings(ctx, plan.TrustedSources, &d)
		}
	}
	publicChanged := current == nil && (plan.PublicAccess.ValueBool() || !plan.PublicSources.IsNull() || plan.PublicIPv6.ValueBool())
	if current != nil {
		publicChanged = !plan.PublicAccess.Equal(current.PublicAccess) || !plan.PublicSources.Equal(current.PublicSources) ||
			!plan.PublicAllowAny.Equal(current.PublicAllowAny) || !plan.PublicIPv6.Equal(current.PublicIPv6)
	}
	if publicChanged {
		sources := listStrings(ctx, plan.PublicSources, &d)
		if sources == nil {
			sources = []string{}
		}
		body["public_access"] = plan.PublicAccess.ValueBool()
		body["public_sources"] = sources
		body["public_allow_any"] = plan.PublicAllowAny.ValueBool()
		body["public_ipv6"] = plan.PublicIPv6.ValueBool()
	}
	if v := optInt(plan.MaintenanceDay); v != nil && (current == nil || !plan.MaintenanceDay.Equal(current.MaintenanceDay)) {
		body["maintenance_day"] = *v
	}
	if v := optInt(plan.MaintenanceHour); v != nil && (current == nil || !plan.MaintenanceHour.Equal(current.MaintenanceHour)) {
		body["maintenance_hour"] = *v
	}
	if v := optBool(plan.AutoMinorUpdates); v != nil && (current == nil || !plan.AutoMinorUpdates.Equal(current.AutoMinorUpdates)) {
		body["auto_minor_updates"] = *v
	}
	return body
}

func (r *databaseResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan databaseModel
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
		"name": plan.Name.ValueString(), "engine": plan.Engine.ValueString(), "version": plan.Version.ValueString(),
		"location_id": plan.LocationID.ValueString(), "vpc_id": plan.VPCID.ValueString(), "plan_id": plan.PlanID.ValueString(),
	}
	if v := optString(plan.StorageClassID); v != nil {
		body["storage_class_id"] = *v
	}
	if sources := listStrings(ctx, plan.TrustedSources, &resp.Diagnostics); sources != nil {
		body["trusted_sources"] = sources
	}
	var out apiDatabase
	if err := r.client.Post(ctx, databasePath+"/create", body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not order database", err, databaseFields)
		return
	}
	desired := plan
	plan.apply(ctx, out)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)

	db, err := r.waitRunning(ctx, out.ID, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Database provisioning did not finish", err.Error())
		return
	}

	// Public access and maintenance are not part of the order: set them once the server runs.
	settings := r.settingsBody(ctx, desired, nil)
	delete(settings, "trusted_sources")
	if len(settings) > 0 {
		if err := r.client.Post(ctx, databasePath+"/"+out.ID+"/update", settings, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not configure database", err, databaseFields)
			return
		}
		if db, err = r.get(ctx, out.ID); err != nil {
			addAPIError(&resp.Diagnostics, "Could not read database", err, nil)
			return
		}
	}
	desired.apply(ctx, db)
	resp.Diagnostics.Append(resp.State.Set(ctx, desired)...)
}

func (r *databaseResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	db, err := r.get(ctx, state.ID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read database", err, nil)
		return
	}
	if managedGone(db.Status) || db.DeletionRequested {
		resp.State.RemoveResource(ctx)
		return
	}
	state.apply(ctx, db)
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *databaseResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state databaseModel
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
	body := r.settingsBody(ctx, plan, &state)
	if !plan.Name.Equal(state.Name) {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.PlanID.Equal(state.PlanID) {
		body["plan_id"] = plan.PlanID.ValueString()
	}
	id := state.ID.ValueString()
	if len(body) > 0 {
		if err := r.client.Post(ctx, databasePath+"/"+id+"/update", body, nil); err != nil {
			addAPIError(&resp.Diagnostics, "Could not update database", err, databaseFields)
			return
		}
	}
	db, err := r.waitRunning(ctx, id, timeout)
	if err != nil {
		resp.Diagnostics.AddError("Database update did not finish", err.Error())
		return
	}
	plan.apply(ctx, db)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *databaseResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state databaseModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, databasePath+"/"+state.ID.ValueString()+"/delete", map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete database", err, nil)
	}
}

func (r *databaseResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// --- logical database ---

var (
	_ resource.Resource                = (*databaseDBResource)(nil)
	_ resource.ResourceWithImportState = (*databaseDBResource)(nil)
)

type databaseDBResource struct{ base }

// NewDatabaseDBResource manages a logical database on a managed database server.
func NewDatabaseDBResource() resource.Resource { return &databaseDBResource{} }

type databaseDBModel struct {
	ID         types.String `tfsdk:"id"`
	DatabaseID types.String `tfsdk:"database_id"`
	Name       types.String `tfsdk:"name"`
}

func (r *databaseDBResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_db"
}

func (r *databaseDBResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A logical database on a `reselling_database` server. Import with `database_id/db_id`.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"database_id": schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Id of the database server."},
			"name": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "Name: starts with a lowercase letter, then lowercase letters, digits and underscores (max. 40).",
			},
		},
	}
}

type apiLogicalDB struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (r *databaseDBResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan databaseDBModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiLogicalDB
	if err := r.client.Post(ctx, databasePath+"/"+plan.DatabaseID.ValueString()+"/databases/create", map[string]any{"name": plan.Name.ValueString()}, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create database", err, fieldPaths{"name": path.Root("name")})
		return
	}
	plan.ID = types.StringValue(out.ID)
	plan.Name = types.StringValue(out.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *databaseDBResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state databaseDBModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Databases []apiLogicalDB `json:"databases"`
	}
	if err := r.client.Get(ctx, databasePath+"/"+state.DatabaseID.ValueString()+"/databases", &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read databases", err, nil)
		return
	}
	for _, db := range out.Databases {
		if db.ID == state.ID.ValueString() {
			state.Name = types.StringValue(db.Name)
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *databaseDBResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Unexpected update", "A logical database cannot be changed in place.")
}

func (r *databaseDBResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state databaseDBModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/databases/%s/delete", databasePath, state.DatabaseID.ValueString(), state.ID.ValueString()), map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete database", err, nil)
	}
}

func (r *databaseDBResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importChild(ctx, req, resp, "database_id", "database_id/db_id")
}

// --- user ---

var (
	_ resource.Resource                = (*databaseUserResource)(nil)
	_ resource.ResourceWithImportState = (*databaseUserResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*databaseUserResource)(nil)
)

type databaseUserResource struct{ base }

// NewDatabaseUserResource manages a user of a managed database server.
func NewDatabaseUserResource() resource.Resource { return &databaseUserResource{} }

type databaseUserModel struct {
	ID         types.String `tfsdk:"id"`
	DatabaseID types.String `tfsdk:"database_id"`
	Username   types.String `tfsdk:"username"`
	Grants     []grantModel `tfsdk:"grants"`
	Password   types.String `tfsdk:"password"`
	Rotate     types.String `tfsdk:"rotate"`
}

type grantModel struct {
	DBID      types.String `tfsdk:"db_id"`
	Privilege types.String `tfsdk:"privilege"`
}

type apiDatabaseUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Grants   []struct {
		DatabaseID string `json:"database_id"`
		Privilege  string `json:"privilege"`
	} `json:"grants"`
}

func (r *databaseUserResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_database_user"
}

func (r *databaseUserResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A user of a `reselling_database` server. The password is generated by the API and returned only once; it is " +
			"kept in the state as a sensitive value. Change `rotate` to any new value to rotate the password. After an import the " +
			"password is unknown until it is rotated. Import with `database_id/user_id`.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"database_id": schema.StringAttribute{Required: true, PlanModifiers: replace, MarkdownDescription: "Id of the database server."},
			"username": schema.StringAttribute{
				Required: true, PlanModifiers: replace,
				MarkdownDescription: "Starts with a lowercase letter, then lowercase letters, digits and underscores.",
			},
			"grants": schema.SetNestedAttribute{
				Optional: true, MarkdownDescription: "Privileges per logical database. Changed in place.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"db_id":     schema.StringAttribute{Required: true, MarkdownDescription: "Id of a `reselling_database_db`."},
						"privilege": schema.StringAttribute{Required: true, MarkdownDescription: "For example `owner`, `read_write` or `read_only`."},
					},
				},
			},
			"password": schema.StringAttribute{
				Computed: true, Sensitive: true, MarkdownDescription: "Password as returned on creation or the last rotation.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"rotate": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Free-form trigger: whenever the value changes, the password is rotated (for example a date or `timestamp()` kept in a variable).",
			},
		},
	}
}

// ModifyPlan marks the password as changing when the rotate trigger changes.
func (r *databaseUserResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var planRotate, stateRotate types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("rotate"), &planRotate)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("rotate"), &stateRotate)...)
	if !planRotate.IsNull() && !planRotate.Equal(stateRotate) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("password"), types.StringUnknown())...)
	}
}

func grantsBody(grants []grantModel) []map[string]any {
	out := []map[string]any{}
	for _, g := range grants {
		out = append(out, map[string]any{"database_id": g.DBID.ValueString(), "privilege": g.Privilege.ValueString()})
	}
	return out
}

func grantsFromAPI(u apiDatabaseUser, configuredNull bool) []grantModel {
	if len(u.Grants) == 0 && configuredNull {
		return nil
	}
	out := make([]grantModel, 0, len(u.Grants))
	for _, g := range u.Grants {
		out = append(out, grantModel{DBID: types.StringValue(g.DatabaseID), Privilege: types.StringValue(g.Privilege)})
	}
	return out
}

var databaseUserFields = fieldPaths{"username": path.Root("username"), "grants": path.Root("grants")}

func (r *databaseUserResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan databaseUserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"username": plan.Username.ValueString()}
	if plan.Grants != nil {
		body["grants"] = grantsBody(plan.Grants)
	}
	var out struct {
		User     apiDatabaseUser `json:"user"`
		Password string          `json:"password"`
	}
	if err := r.client.Post(ctx, databasePath+"/"+plan.DatabaseID.ValueString()+"/users/create", body, &out); err != nil {
		addAPIError(&resp.Diagnostics, "Could not create database user", err, databaseUserFields)
		return
	}
	plan.ID = types.StringValue(out.User.ID)
	plan.Username = types.StringValue(out.User.Username)
	plan.Grants = grantsFromAPI(out.User, plan.Grants == nil)
	plan.Password = types.StringValue(out.Password)
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *databaseUserResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state databaseUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Users []apiDatabaseUser `json:"users"`
	}
	if err := r.client.Get(ctx, databasePath+"/"+state.DatabaseID.ValueString()+"/users", &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		addAPIError(&resp.Diagnostics, "Could not read database users", err, nil)
		return
	}
	for _, u := range out.Users {
		if u.ID == state.ID.ValueString() {
			state.Username = types.StringValue(u.Username)
			state.Grants = grantsFromAPI(u, state.Grants == nil)
			resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *databaseUserResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state databaseUserModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	base := fmt.Sprintf("%s/%s/users/%s", databasePath, state.DatabaseID.ValueString(), state.ID.ValueString())
	var updated apiDatabaseUser
	if err := r.client.Post(ctx, base+"/update", map[string]any{"grants": grantsBody(plan.Grants)}, &updated); err != nil {
		addAPIError(&resp.Diagnostics, "Could not update database user", err, databaseUserFields)
		return
	}
	plan.Grants = grantsFromAPI(updated, plan.Grants == nil)
	plan.Password = state.Password
	if !plan.Rotate.Equal(state.Rotate) && !plan.Rotate.IsNull() {
		var out struct {
			Password string `json:"password"`
		}
		if err := r.client.Post(ctx, base+"/rotate-password", map[string]any{}, &out); err != nil {
			addAPIError(&resp.Diagnostics, "Could not rotate the password", err, nil)
			return
		}
		plan.Password = types.StringValue(out.Password)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *databaseUserResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state databaseUserModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.Post(ctx, fmt.Sprintf("%s/%s/users/%s/delete", databasePath, state.DatabaseID.ValueString(), state.ID.ValueString()), map[string]any{}, nil); err != nil && !client.IsNotFound(err) {
		addAPIError(&resp.Diagnostics, "Could not delete database user", err, nil)
	}
}

func (r *databaseUserResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importChild(ctx, req, resp, "database_id", "database_id/user_id")
}
