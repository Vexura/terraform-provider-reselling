package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

// containsFold is a case-insensitive substring match; an empty filter matches everything.
func containsFold(value string, filter types.String) bool {
	if filter.IsNull() || filter.IsUnknown() || filter.ValueString() == "" {
		return true
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(filter.ValueString()))
}

func str(s string) types.String { return types.StringValue(s) }

func i64(i int64) types.Int64 { return types.Int64Value(i) }

func addReadError(diags interface{ AddError(string, string) }, what string, err error) {
	diags.AddError("Could not read "+what, err.Error())
}

// --- locations ---

type locationsDataSource struct{ dataBase }

// NewLocationsDataSource lists rootserver locations with their clusters.
func NewLocationsDataSource() datasource.DataSource { return &locationsDataSource{} }

type locationsModel struct {
	Name      types.String    `tfsdk:"name"`
	Locations []locationModel `tfsdk:"locations"`
}

type locationModel struct {
	ID          types.String   `tfsdk:"id"`
	Name        types.String   `tfsdk:"name"`
	City        types.String   `tfsdk:"city"`
	CountryCode types.String   `tfsdk:"country_code"`
	Clusters    []clusterModel `tfsdk:"clusters"`
}

type clusterModel struct {
	ID             types.String         `tfsdk:"id"`
	Name           types.String         `tfsdk:"name"`
	CPU            types.String         `tfsdk:"cpu"`
	MinCores       types.Int64          `tfsdk:"min_cores"`
	MaxCores       types.Int64          `tfsdk:"max_cores"`
	MinRAM         types.Int64          `tfsdk:"min_ram"`
	MaxRAM         types.Int64          `tfsdk:"max_ram"`
	MinDisk        types.Int64          `tfsdk:"min_disk"`
	MaxDisk        types.Int64          `tfsdk:"max_disk"`
	DiskSteps      types.Int64          `tfsdk:"disk_steps"`
	MaxIPv4        types.Int64          `tfsdk:"max_ipv4"`
	MaxIPv6        types.Int64          `tfsdk:"max_ipv6"`
	BandwidthTiers []bandwidthTierModel `tfsdk:"bandwidth_tiers"`
}

type bandwidthTierModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	SpeedMbps types.Int64  `tfsdk:"speed_mbps"`
	IsDefault types.Bool   `tfsdk:"is_default"`
}

func (d *locationsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_locations"
}

func (d *locationsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Rootserver locations with their hardware clusters and bandwidth tiers.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Optional: true, MarkdownDescription: "Only locations whose name or city contains this text (case-insensitive)."},
			"locations": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":           schema.StringAttribute{Computed: true},
					"name":         schema.StringAttribute{Computed: true},
					"city":         schema.StringAttribute{Computed: true},
					"country_code": schema.StringAttribute{Computed: true},
					"clusters": schema.ListNestedAttribute{
						Computed: true,
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"id":         schema.StringAttribute{Computed: true},
							"name":       schema.StringAttribute{Computed: true},
							"cpu":        schema.StringAttribute{Computed: true},
							"min_cores":  schema.Int64Attribute{Computed: true},
							"max_cores":  schema.Int64Attribute{Computed: true},
							"min_ram":    schema.Int64Attribute{Computed: true, MarkdownDescription: "MB."},
							"max_ram":    schema.Int64Attribute{Computed: true, MarkdownDescription: "MB."},
							"min_disk":   schema.Int64Attribute{Computed: true, MarkdownDescription: "GB."},
							"max_disk":   schema.Int64Attribute{Computed: true, MarkdownDescription: "GB."},
							"disk_steps": schema.Int64Attribute{Computed: true},
							"max_ipv4":   schema.Int64Attribute{Computed: true},
							"max_ipv6":   schema.Int64Attribute{Computed: true},
							"bandwidth_tiers": schema.ListNestedAttribute{
								Computed: true,
								NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
									"id":         schema.StringAttribute{Computed: true},
									"name":       schema.StringAttribute{Computed: true},
									"speed_mbps": schema.Int64Attribute{Computed: true},
									"is_default": schema.BoolAttribute{Computed: true},
								}},
							},
						}},
					},
				}},
			},
		},
	}
}

func (d *locationsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg locationsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Locations []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			City        string `json:"city"`
			CountryCode string `json:"country_code"`
			Clusters    []struct {
				ID         string `json:"id"`
				Name       string `json:"name"`
				SystemInfo struct {
					CPU string `json:"cpu"`
				} `json:"system_info"`
				MinCores       int64 `json:"min_cores"`
				MaxCores       int64 `json:"max_cores"`
				MinRAM         int64 `json:"min_ram"`
				MaxRAM         int64 `json:"max_ram"`
				MinDisk        int64 `json:"min_disk"`
				MaxDisk        int64 `json:"max_disk"`
				DiskSteps      int64 `json:"disk_steps"`
				MaxIPv4        int64 `json:"max_ipv4"`
				MaxIPv6        int64 `json:"max_ipv6"`
				BandwidthTiers []struct {
					ID        string `json:"id"`
					Name      string `json:"name"`
					SpeedMbps int64  `json:"speed_mbps"`
					IsDefault bool   `json:"is_default"`
				} `json:"bandwidth_tiers"`
			} `json:"clusters"`
		} `json:"locations"`
	}
	if err := d.client.Get(ctx, rootserverPath+"/locations", &out); err != nil {
		addReadError(&resp.Diagnostics, "locations", err)
		return
	}
	cfg.Locations = []locationModel{}
	for _, l := range out.Locations {
		if !containsFold(l.Name+" "+l.City, cfg.Name) {
			continue
		}
		loc := locationModel{ID: str(l.ID), Name: str(l.Name), City: str(l.City), CountryCode: str(l.CountryCode), Clusters: []clusterModel{}}
		for _, c := range l.Clusters {
			cm := clusterModel{
				ID: str(c.ID), Name: str(c.Name), CPU: str(c.SystemInfo.CPU), MinCores: i64(c.MinCores), MaxCores: i64(c.MaxCores),
				MinRAM: i64(c.MinRAM), MaxRAM: i64(c.MaxRAM), MinDisk: i64(c.MinDisk), MaxDisk: i64(c.MaxDisk), DiskSteps: i64(c.DiskSteps),
				MaxIPv4: i64(c.MaxIPv4), MaxIPv6: i64(c.MaxIPv6), BandwidthTiers: []bandwidthTierModel{},
			}
			for _, t := range c.BandwidthTiers {
				cm.BandwidthTiers = append(cm.BandwidthTiers, bandwidthTierModel{ID: str(t.ID), Name: str(t.Name), SpeedMbps: i64(t.SpeedMbps), IsDefault: types.BoolValue(t.IsDefault)})
			}
			loc.Clusters = append(loc.Clusters, cm)
		}
		cfg.Locations = append(cfg.Locations, loc)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, cfg)...)
}

// --- OS versions ---

type osVersionsDataSource struct{ dataBase }

// NewOSVersionsDataSource lists installable operating system versions.
func NewOSVersionsDataSource() datasource.DataSource { return &osVersionsDataSource{} }

type osVersionsModel struct {
	Name     types.String     `tfsdk:"name"`
	Version  types.String     `tfsdk:"version"`
	Versions []osVersionModel `tfsdk:"versions"`
}

type osVersionModel struct {
	ID        types.String `tfsdk:"id"`
	OSName    types.String `tfsdk:"os_name"`
	Version   types.String `tfsdk:"version"`
	Type      types.String `tfsdk:"type"`
	MinCores  types.Int64  `tfsdk:"min_cores"`
	MinRAMGB  types.Int64  `tfsdk:"min_ram_gb"`
	MinDiskGB types.Int64  `tfsdk:"min_disk_gb"`
}

func (d *osVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_os_versions"
}

func (d *osVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Installable operating system versions and app templates; their `id` is the `os_version_id` of a rootserver.",
		Attributes: map[string]schema.Attribute{
			"name":    schema.StringAttribute{Optional: true, MarkdownDescription: "Only systems whose name contains this text (case-insensitive), for example `debian`."},
			"version": schema.StringAttribute{Optional: true, MarkdownDescription: "Only this exact version, for example `12`."},
			"versions": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":          schema.StringAttribute{Computed: true},
					"os_name":     schema.StringAttribute{Computed: true},
					"version":     schema.StringAttribute{Computed: true},
					"type":        schema.StringAttribute{Computed: true, MarkdownDescription: "`os` or `app`."},
					"min_cores":   schema.Int64Attribute{Computed: true},
					"min_ram_gb":  schema.Int64Attribute{Computed: true},
					"min_disk_gb": schema.Int64Attribute{Computed: true},
				}},
			},
		},
	}
}

func (d *osVersionsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg osVersionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		OperatingSystems []struct {
			Name     string `json:"name"`
			Versions []struct {
				ID        string `json:"id"`
				Version   string `json:"version"`
				MinCores  *int64 `json:"min_cores"`
				MinRAMGB  *int64 `json:"min_ram_gb"`
				MinDiskGB *int64 `json:"min_disk_gb"`
			} `json:"versions"`
		} `json:"operating_systems"`
		Apps []struct {
			Name      string `json:"name"`
			VersionID string `json:"version_id"`
			MinCores  *int64 `json:"min_cores"`
			MinRAMGB  *int64 `json:"min_ram_gb"`
			MinDiskGB *int64 `json:"min_disk_gb"`
		} `json:"apps"`
	}
	if err := d.client.Get(ctx, rootserverPath+"/os-list", &out); err != nil {
		addReadError(&resp.Diagnostics, "operating systems", err)
		return
	}
	cfg.Versions = []osVersionModel{}
	versionMatches := func(v string) bool {
		return cfg.Version.IsNull() || cfg.Version.ValueString() == "" || cfg.Version.ValueString() == v
	}
	for _, os := range out.OperatingSystems {
		if !containsFold(os.Name, cfg.Name) {
			continue
		}
		for _, v := range os.Versions {
			if !versionMatches(v.Version) {
				continue
			}
			cfg.Versions = append(cfg.Versions, osVersionModel{
				ID: str(v.ID), OSName: str(os.Name), Version: str(v.Version), Type: str("os"),
				MinCores: intOrNull(v.MinCores), MinRAMGB: intOrNull(v.MinRAMGB), MinDiskGB: intOrNull(v.MinDiskGB),
			})
		}
	}
	for _, app := range out.Apps {
		if app.VersionID == "" || !containsFold(app.Name, cfg.Name) || (!cfg.Version.IsNull() && cfg.Version.ValueString() != "") {
			continue
		}
		cfg.Versions = append(cfg.Versions, osVersionModel{
			ID: str(app.VersionID), OSName: str(app.Name), Version: types.StringNull(), Type: str("app"),
			MinCores: intOrNull(app.MinCores), MinRAMGB: intOrNull(app.MinRAMGB), MinDiskGB: intOrNull(app.MinDiskGB),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, cfg)...)
}

// --- images ---

type imagesDataSource struct{ dataBase }

// NewImagesDataSource lists custom images.
func NewImagesDataSource() datasource.DataSource { return &imagesDataSource{} }

type imagesModel struct {
	Name   types.String     `tfsdk:"name"`
	Images []imageListModel `tfsdk:"images"`
}

type imageListModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Status      types.String `tfsdk:"status"`
	OSVersionID types.String `tfsdk:"os_version_id"`
	ClusterID   types.String `tfsdk:"cluster_id"`
	LocationID  types.String `tfsdk:"location_id"`
	SizeGB      types.Int64  `tfsdk:"size_gb"`
	MinDiskGB   types.Int64  `tfsdk:"min_disk_gb"`
	Owned       types.Bool   `tfsdk:"owned"`
	Labels      types.List   `tfsdk:"labels"`
}

func (d *imagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_images"
}

func (d *imagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Custom images visible to the account (own images and, for a sub-reseller, images shared by the main reseller).",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{Optional: true, MarkdownDescription: "Only images whose name contains this text (case-insensitive)."},
			"images": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":            schema.StringAttribute{Computed: true},
					"name":          schema.StringAttribute{Computed: true},
					"status":        schema.StringAttribute{Computed: true},
					"os_version_id": schema.StringAttribute{Computed: true},
					"cluster_id":    schema.StringAttribute{Computed: true},
					"location_id":   schema.StringAttribute{Computed: true},
					"size_gb":       schema.Int64Attribute{Computed: true},
					"min_disk_gb":   schema.Int64Attribute{Computed: true},
					"owned":         schema.BoolAttribute{Computed: true},
					"labels":        schema.ListAttribute{Computed: true, ElementType: types.StringType},
				}},
			},
		},
	}
}

func (d *imagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg imagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Images []apiImage `json:"images"`
	}
	if err := d.client.Get(ctx, imagesPath, &out); err != nil {
		addReadError(&resp.Diagnostics, "images", err)
		return
	}
	cfg.Images = []imageListModel{}
	for _, img := range out.Images {
		if !containsFold(img.Name, cfg.Name) {
			continue
		}
		labels := img.Labels
		if labels == nil {
			labels = []string{}
		}
		cfg.Images = append(cfg.Images, imageListModel{
			ID: str(img.ID), Name: str(img.Name), Status: str(img.Status), OSVersionID: refID(img.OS), ClusterID: refID(img.Cluster),
			LocationID: refID(img.Location), SizeGB: intOrNull(img.SizeGB), MinDiskGB: intOrNull(img.MinDiskGB),
			Owned: types.BoolValue(img.Owned), Labels: stringList(labels),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, cfg)...)
}

// --- storage classes ---

type storageClassesDataSource struct{ dataBase }

// NewStorageClassesDataSource lists the volume storage classes of a cluster.
func NewStorageClassesDataSource() datasource.DataSource { return &storageClassesDataSource{} }

type storageClassesModel struct {
	ClusterID      types.String        `tfsdk:"cluster_id"`
	StorageClasses []storageClassModel `tfsdk:"storage_classes"`
}

type storageClassModel struct {
	ID                types.String  `tfsdk:"id"`
	Name              types.String  `tfsdk:"name"`
	IsSSD             types.Bool    `tfsdk:"is_ssd"`
	MinSizeGB         types.Int64   `tfsdk:"min_size_gb"`
	MaxSizeGB         types.Int64   `tfsdk:"max_size_gb"`
	StepGB            types.Int64   `tfsdk:"step_gb"`
	PricePerGBMonthly types.Float64 `tfsdk:"price_per_gb_monthly"`
}

func (d *storageClassesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_storage_classes"
}

func (d *storageClassesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Storage classes volumes can be created in on a cluster, with size limits and the monthly price per GB.",
		Attributes: map[string]schema.Attribute{
			"cluster_id": schema.StringAttribute{Required: true},
			"storage_classes": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                   schema.StringAttribute{Computed: true},
					"name":                 schema.StringAttribute{Computed: true},
					"is_ssd":               schema.BoolAttribute{Computed: true},
					"min_size_gb":          schema.Int64Attribute{Computed: true},
					"max_size_gb":          schema.Int64Attribute{Computed: true},
					"step_gb":              schema.Int64Attribute{Computed: true},
					"price_per_gb_monthly": schema.Float64Attribute{Computed: true},
				}},
			},
		},
	}
}

func (d *storageClassesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg storageClassesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		StorageClasses []struct {
			ID                string  `json:"id"`
			Name              string  `json:"name"`
			IsSSD             bool    `json:"is_ssd"`
			MinSizeGB         *int64  `json:"min_size_gb"`
			MaxSizeGB         *int64  `json:"max_size_gb"`
			StepGB            *int64  `json:"step_gb"`
			PricePerGBMonthly float64 `json:"price_per_gb_monthly"`
		} `json:"storage_classes"`
	}
	if err := d.client.Get(ctx, volumesPath+"/storage-classes/"+url.PathEscape(cfg.ClusterID.ValueString()), &out); err != nil {
		addReadError(&resp.Diagnostics, "storage classes", err)
		return
	}
	cfg.StorageClasses = []storageClassModel{}
	for _, c := range out.StorageClasses {
		cfg.StorageClasses = append(cfg.StorageClasses, storageClassModel{
			ID: str(c.ID), Name: str(c.Name), IsSSD: types.BoolValue(c.IsSSD), MinSizeGB: intOrNull(c.MinSizeGB),
			MaxSizeGB: intOrNull(c.MaxSizeGB), StepGB: intOrNull(c.StepGB), PricePerGBMonthly: types.Float64Value(c.PricePerGBMonthly),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, cfg)...)
}

// --- plans (load balancer, database, kubernetes) ---

type planModel struct {
	ID           types.String  `tfsdk:"id"`
	Name         types.String  `tfsdk:"name"`
	DisplayName  types.String  `tfsdk:"display_name"`
	Cores        types.Int64   `tfsdk:"cores"`
	MemoryMB     types.Int64   `tfsdk:"memory_mb"`
	DiskGB       types.Int64   `tfsdk:"disk_gb"`
	MonthlyPrice types.Float64 `tfsdk:"monthly_price"`
}

type apiPlan struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	DisplayName  *string  `json:"display_name"`
	Cores        *int64   `json:"cores"`
	MemoryMB     *int64   `json:"memory_mb"`
	RAMMB        *int64   `json:"ram_mb"`
	DiskGB       *int64   `json:"disk_gb"`
	MonthlyPrice *float64 `json:"monthly_price"`
	PriceMonthly *float64 `json:"price_monthly"`
	IsCP         *bool    `json:"is_control_plane"`
}

func (p apiPlan) model() planModel {
	memory := p.MemoryMB
	if memory == nil {
		memory = p.RAMMB
	}
	price := p.MonthlyPrice
	if price == nil {
		price = p.PriceMonthly
	}
	priceValue := types.Float64Null()
	if price != nil {
		priceValue = types.Float64Value(*price)
	}
	return planModel{
		ID: str(p.ID), Name: str(p.Name), DisplayName: strOrNull(p.DisplayName), Cores: intOrNull(p.Cores),
		MemoryMB: intOrNull(memory), DiskGB: intOrNull(p.DiskGB), MonthlyPrice: priceValue,
	}
}

func planAttributes(extra map[string]schema.Attribute) map[string]schema.Attribute {
	attrs := map[string]schema.Attribute{
		"id":            schema.StringAttribute{Computed: true},
		"name":          schema.StringAttribute{Computed: true},
		"display_name":  schema.StringAttribute{Computed: true},
		"cores":         schema.Int64Attribute{Computed: true},
		"memory_mb":     schema.Int64Attribute{Computed: true},
		"disk_gb":       schema.Int64Attribute{Computed: true},
		"monthly_price": schema.Float64Attribute{Computed: true, MarkdownDescription: "Your monthly price in EUR."},
	}
	for k, v := range extra {
		attrs[k] = v
	}
	return attrs
}

type plansDataSource struct {
	dataBase
	typeName    string
	path        string
	description string
	kubernetes  bool
	database    bool
}

// NewLoadBalancerPlansDataSource lists load balancer plans.
func NewLoadBalancerPlansDataSource() datasource.DataSource {
	return &plansDataSource{typeName: "_load_balancer_plans", path: lbPath + "/plans", description: "Active load balancer plans with your monthly price."}
}

// NewDatabasePlansDataSource lists managed database plans and engine versions.
func NewDatabasePlansDataSource() datasource.DataSource {
	return &plansDataSource{typeName: "_database_plans", path: databasePath + "/plans", database: true,
		description: "Active managed database plans with your monthly price, and the engine versions that can be ordered."}
}

// NewKubernetesPlansDataSource lists Kubernetes node plans.
func NewKubernetesPlansDataSource() datasource.DataSource {
	return &plansDataSource{typeName: "_kubernetes_plans", path: kubernetesPath + "/plans", kubernetes: true,
		description: "Kubernetes control plane and worker plans with their monthly price."}
}

func (d *plansDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + d.typeName
}

func (d *plansDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	extra := map[string]schema.Attribute{}
	if d.kubernetes {
		extra["is_control_plane"] = schema.BoolAttribute{Computed: true}
	}
	attrs := map[string]schema.Attribute{
		"name": schema.StringAttribute{Optional: true, MarkdownDescription: "Only plans whose name contains this text (case-insensitive)."},
		"plans": schema.ListNestedAttribute{
			Computed:     true,
			NestedObject: schema.NestedAttributeObject{Attributes: planAttributes(extra)},
		},
	}
	if d.database {
		attrs["engines"] = schema.ListNestedAttribute{
			Computed: true,
			NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"engine":  schema.StringAttribute{Computed: true},
				"version": schema.StringAttribute{Computed: true},
				"label":   schema.StringAttribute{Computed: true},
			}},
		}
	}
	resp.Schema = schema.Schema{MarkdownDescription: d.description, Attributes: attrs}
}

var planObjectType = map[string]attr.Type{
	"id": types.StringType, "name": types.StringType, "display_name": types.StringType, "cores": types.Int64Type,
	"memory_mb": types.Int64Type, "disk_gb": types.Int64Type, "monthly_price": types.Float64Type,
}

func (d *plansDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var filter types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &filter)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Plans   []apiPlan `json:"plans"`
		Engines []struct {
			Engine  string `json:"engine"`
			Version string `json:"version"`
			Label   string `json:"label"`
		} `json:"engines"`
	}
	if err := d.client.Get(ctx, d.path, &out); err != nil {
		addReadError(&resp.Diagnostics, "plans", err)
		return
	}

	types_ := map[string]attr.Type{}
	for k, v := range planObjectType {
		types_[k] = v
	}
	if d.kubernetes {
		types_["is_control_plane"] = types.BoolType
	}
	elems := []attr.Value{}
	for _, p := range out.Plans {
		if !containsFold(p.Name+" "+deref(p.DisplayName), filter) {
			continue
		}
		m := p.model()
		values := map[string]attr.Value{
			"id": m.ID, "name": m.Name, "display_name": m.DisplayName, "cores": m.Cores, "memory_mb": m.MemoryMB,
			"disk_gb": m.DiskGB, "monthly_price": m.MonthlyPrice,
		}
		if d.kubernetes {
			values["is_control_plane"] = boolOrNull(p.IsCP)
		}
		obj, diags := types.ObjectValue(types_, values)
		resp.Diagnostics.Append(diags...)
		elems = append(elems, obj)
	}
	list, diags := types.ListValue(types.ObjectType{AttrTypes: types_}, elems)
	resp.Diagnostics.Append(diags...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), filter)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("plans"), list)...)

	if d.database {
		engineType := map[string]attr.Type{"engine": types.StringType, "version": types.StringType, "label": types.StringType}
		engines := []attr.Value{}
		for _, e := range out.Engines {
			obj, diags := types.ObjectValue(engineType, map[string]attr.Value{"engine": str(e.Engine), "version": str(e.Version), "label": str(e.Label)})
			resp.Diagnostics.Append(diags...)
			engines = append(engines, obj)
		}
		list, diags := types.ListValue(types.ObjectType{AttrTypes: engineType}, engines)
		resp.Diagnostics.Append(diags...)
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("engines"), list)...)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// --- kubernetes versions ---

type kubernetesVersionsDataSource struct{ dataBase }

// NewKubernetesVersionsDataSource lists installable Kubernetes versions.
func NewKubernetesVersionsDataSource() datasource.DataSource { return &kubernetesVersionsDataSource{} }

type kubernetesVersionsModel struct {
	Versions []kubernetesVersionModel `tfsdk:"versions"`
	Default  types.String             `tfsdk:"default_id"`
}

type kubernetesVersionModel struct {
	ID          types.String `tfsdk:"id"`
	Version     types.String `tfsdk:"version"`
	IsDefault   types.Bool   `tfsdk:"is_default"`
	EndOfLifeAt types.String `tfsdk:"end_of_life_at"`
}

func (d *kubernetesVersionsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_versions"
}

func (d *kubernetesVersionsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Kubernetes versions that can be installed.",
		Attributes: map[string]schema.Attribute{
			"default_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Id of the default version."},
			"versions": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":             schema.StringAttribute{Computed: true},
					"version":        schema.StringAttribute{Computed: true},
					"is_default":     schema.BoolAttribute{Computed: true},
					"end_of_life_at": schema.StringAttribute{Computed: true},
				}},
			},
		},
	}
}

func (d *kubernetesVersionsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var out struct {
		Versions []struct {
			ID          string  `json:"id"`
			Version     string  `json:"version"`
			IsDefault   bool    `json:"is_default"`
			EndOfLifeAt *string `json:"end_of_life_at"`
		} `json:"versions"`
	}
	if err := d.client.Get(ctx, kubernetesPath+"/versions", &out); err != nil {
		addReadError(&resp.Diagnostics, "Kubernetes versions", err)
		return
	}
	m := kubernetesVersionsModel{Versions: []kubernetesVersionModel{}, Default: types.StringNull()}
	for _, v := range out.Versions {
		m.Versions = append(m.Versions, kubernetesVersionModel{ID: str(v.ID), Version: str(v.Version), IsDefault: types.BoolValue(v.IsDefault), EndOfLifeAt: strOrNull(v.EndOfLifeAt)})
		if v.IsDefault {
			m.Default = str(v.ID)
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// --- rootserver ---

type rootserverDataSource struct{ dataBase }

// NewRootserverDataSource looks up a rootserver by id or hostname.
func NewRootserverDataSource() datasource.DataSource { return &rootserverDataSource{} }

type rootserverDataModel struct {
	ID            types.String `tfsdk:"id"`
	Hostname      types.String `tfsdk:"hostname"`
	Status        types.String `tfsdk:"status"`
	Cores         types.Int64  `tfsdk:"cores"`
	RAM           types.Int64  `tfsdk:"ram"`
	Disk          types.Int64  `tfsdk:"disk"`
	PrimaryIP     types.String `tfsdk:"primary_ip"`
	LocationID    types.String `tfsdk:"location_id"`
	ClusterID     types.String `tfsdk:"cluster_id"`
	OSVersionID   types.String `tfsdk:"os_version_id"`
	Labels        types.List   `tfsdk:"labels"`
	IPv4Addresses types.List   `tfsdk:"ipv4_addresses"`
	IPv6Subnets   types.List   `tfsdk:"ipv6_subnets"`
}

func (d *rootserverDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_rootserver"
}

func (d *rootserverDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up an existing rootserver by `id` or by exact `hostname`.",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Optional: true, Computed: true},
			"hostname":       schema.StringAttribute{Optional: true, Computed: true},
			"status":         schema.StringAttribute{Computed: true},
			"cores":          schema.Int64Attribute{Computed: true},
			"ram":            schema.Int64Attribute{Computed: true},
			"disk":           schema.Int64Attribute{Computed: true},
			"primary_ip":     schema.StringAttribute{Computed: true},
			"location_id":    schema.StringAttribute{Computed: true},
			"cluster_id":     schema.StringAttribute{Computed: true},
			"os_version_id":  schema.StringAttribute{Computed: true},
			"labels":         schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"ipv4_addresses": schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"ipv6_subnets":   schema.ListAttribute{Computed: true, ElementType: types.StringType},
		},
	}
}

func (d *rootserverDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg rootserverDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := cfg.ID.ValueString()
	if id == "" {
		hostname := cfg.Hostname.ValueString()
		if hostname == "" {
			resp.Diagnostics.AddError("Missing lookup key", "Set id or hostname.")
			return
		}
		var list struct {
			Servers []apiRootserver `json:"servers"`
		}
		if err := d.client.Get(ctx, rootserverPath+"?per_page=100&search="+url.QueryEscape(hostname), &list); err != nil {
			addReadError(&resp.Diagnostics, "rootservers", err)
			return
		}
		matches := []string{}
		for _, s := range list.Servers {
			if strings.EqualFold(s.HostName, hostname) {
				matches = append(matches, s.ID)
			}
		}
		switch len(matches) {
		case 0:
			resp.Diagnostics.AddAttributeError(path.Root("hostname"), "Rootserver not found", fmt.Sprintf("No rootserver has the hostname %q.", hostname))
			return
		case 1:
			id = matches[0]
		default:
			resp.Diagnostics.AddAttributeError(path.Root("hostname"), "Hostname is ambiguous",
				fmt.Sprintf("%d rootservers have the hostname %q; look the server up by id instead.", len(matches), hostname))
			return
		}
	}
	r := &rootserverResource{base: base{client: d.client}}
	s, err := r.get(ctx, id)
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("id"), "Rootserver not found", err.Error())
			return
		}
		addReadError(&resp.Diagnostics, "rootserver", err)
		return
	}
	v4, v6 := []string{}, []string{}
	for _, ip := range s.IPv4Addresses {
		v4 = append(v4, ip.Address)
	}
	for _, sn := range s.IPv6Subnets {
		v6 = append(v6, sn.Subnet)
	}
	labels := s.Labels
	if labels == nil {
		labels = []string{}
	}
	m := rootserverDataModel{
		ID: str(s.ID), Hostname: str(s.HostName), Status: str(s.Status), Cores: i64(s.Cores), RAM: i64(s.RAM), Disk: i64(s.Disk),
		PrimaryIP: strOrNull(s.PrimaryIP), LocationID: str(s.LocationID), ClusterID: str(s.ClusterID), OSVersionID: refID(s.OS),
		Labels: stringList(labels), IPv4Addresses: stringList(v4), IPv6Subnets: stringList(v6),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, m)...)
}

// --- kubeconfig ---

type kubeconfigDataSource struct{ dataBase }

// NewKubeconfigDataSource returns the kubeconfig of a Kubernetes cluster.
func NewKubeconfigDataSource() datasource.DataSource { return &kubeconfigDataSource{} }

type kubeconfigModel struct {
	ClusterID  types.String `tfsdk:"cluster_id"`
	Kubeconfig types.String `tfsdk:"kubeconfig"`
}

func (d *kubeconfigDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_kubernetes_kubeconfig"
}

func (d *kubeconfigDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The admin kubeconfig of a Kubernetes cluster. It is stored in the state as a sensitive value; protect the state accordingly.",
		Attributes: map[string]schema.Attribute{
			"cluster_id": schema.StringAttribute{Required: true},
			"kubeconfig": schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "kubeconfig YAML."},
		},
	}
}

func (d *kubeconfigDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg kubeconfigModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := d.client.Get(ctx, kubernetesPath+"/"+cfg.ClusterID.ValueString()+"/kubeconfig", &out); err != nil {
		addReadError(&resp.Diagnostics, "kubeconfig", err)
		return
	}
	cfg.Kubeconfig = types.StringValue(out.Kubeconfig)
	resp.Diagnostics.Append(resp.State.Set(ctx, cfg)...)
}
