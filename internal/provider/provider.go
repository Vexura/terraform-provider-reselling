// Package provider implements the Terraform provider for the reseller platform's public API v1.
package provider

import (
	"context"
	"os"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vexura/terraform-provider-reselling/internal/client"
)

// APIKeyEnv is the environment variable the API key is read from when api_key is not set.
const APIKeyEnv = "RESELLING_API_KEY"

// EndpointEnv overrides the endpoint when the endpoint attribute is not set.
const EndpointEnv = "RESELLING_ENDPOINT"

var _ provider.Provider = (*resellingProvider)(nil)

type resellingProvider struct {
	version string
}

type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	APIKey   types.String `tfsdk:"api_key"`
	Timeout  types.String `tfsdk:"timeout"`
}

// New returns the provider factory used by main and by the tests.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &resellingProvider{version: version}
	}
}

func (p *resellingProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "reselling"
	resp.Version = p.version
}

func (p *resellingProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages rootservers, networks, load balancers, managed databases and Kubernetes clusters " +
			"of the reseller platform through its public API v1.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Base URL of the API, defaults to `" + client.DefaultEndpoint + "` " +
					"(or the `" + EndpointEnv + "` environment variable). Use the API host: the main domain " +
					"is behind a bot protection that refuses automated clients.",
			},
			"api_key": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "API key, sent as `Authorization: Bearer <key>`. Defaults to the `" + APIKeyEnv +
					"` environment variable. The key needs the permissions of the resources you manage " +
					"(for example `rootservers:read`, `rootservers:create`, `rootservers:update`, `rootservers:delete`).",
			},
			"timeout": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Timeout of a single HTTP request as a Go duration such as `60s` (default). " +
					"Waiting for provisioning is configured per resource with `timeouts` blocks.",
			},
		},
	}
}

func (p *resellingProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.APIKey.IsUnknown() || config.Endpoint.IsUnknown() || config.Timeout.IsUnknown() {
		resp.Diagnostics.AddWarning("Provider configuration not known yet",
			"api_key, endpoint or timeout depends on a value that is only known after apply. The provider will be configured during apply.")
		return
	}

	endpoint := os.Getenv(EndpointEnv)
	if !config.Endpoint.IsNull() {
		endpoint = config.Endpoint.ValueString()
	}
	apiKey := os.Getenv(APIKeyEnv)
	if !config.APIKey.IsNull() {
		apiKey = config.APIKey.ValueString()
	}
	if apiKey == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_key"), "Missing API key",
			"Set api_key in the provider block or the "+APIKeyEnv+" environment variable.")
		return
	}

	timeout := 60 * time.Second
	if !config.Timeout.IsNull() {
		parsed, err := time.ParseDuration(config.Timeout.ValueString())
		if err != nil || parsed <= 0 {
			resp.Diagnostics.AddAttributeError(path.Root("timeout"), "Invalid timeout",
				"timeout must be a positive Go duration such as 60s or 2m.")
			return
		}
		timeout = parsed
	}

	c, err := client.New(endpoint, apiKey, timeout, "terraform-provider-reselling/"+p.version)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Invalid provider configuration", err.Error())
		return
	}
	c.Logger = func(ctx context.Context, msg string, fields map[string]any) {
		tflog.Debug(ctx, msg, fields)
	}
	applyTestTuning(c)

	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *resellingProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewSSHKeyResource,
		NewStartupScriptResource,
		NewRootserverResource,
		NewVolumeResource,
		NewVolumeAttachmentResource,
		NewVPCResource,
		NewVPCRouteResource,
		NewVPCMemberResource,
		NewFirewallGroupResource,
		NewRootserverFirewallResource,
		NewLoadBalancerResource,
		NewLoadBalancerServiceResource,
		NewLoadBalancerTargetResource,
		NewLoadBalancerCertificateResource,
		NewDatabaseResource,
		NewDatabaseDBResource,
		NewDatabaseUserResource,
		NewKubernetesClusterResource,
		NewKubernetesAddonResource,
		NewRootserverImageResource,
		NewImageRecipeResource,
	}
}

func (p *resellingProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewLocationsDataSource,
		NewOSVersionsDataSource,
		NewImagesDataSource,
		NewStorageClassesDataSource,
		NewLoadBalancerPlansDataSource,
		NewDatabasePlansDataSource,
		NewKubernetesPlansDataSource,
		NewKubernetesVersionsDataSource,
		NewRootserverDataSource,
		NewKubeconfigDataSource,
	}
}
