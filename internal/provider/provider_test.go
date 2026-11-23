package provider

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	helper "github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// TestProviderSchemas lets the framework validate every provider, resource and data source schema.
func TestProviderSchemas(t *testing.T) {
	server := providerserver.NewProtocol6(New("test")())()
	resp, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range resp.Diagnostics {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			t.Errorf("schema error: %s: %s", d.Summary, d.Detail)
		}
	}
	if len(resp.ResourceSchemas) != 21 {
		t.Errorf("expected 21 resources, got %d", len(resp.ResourceSchemas))
	}
	if len(resp.DataSourceSchemas) != 10 {
		t.Errorf("expected 10 data sources, got %d", len(resp.DataSourceSchemas))
	}

	// Every resource and data source validates its own schema implementation as well.
	p := New("test")()
	for _, factory := range p.Resources(context.Background()) {
		r := factory()
		var schemaResp resource.SchemaResponse
		r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
		if schemaResp.Diagnostics.HasError() {
			t.Errorf("%T: %v", r, schemaResp.Diagnostics)
		}
		if diags := schemaResp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
			t.Errorf("%T: %v", r, diags)
		}
	}
	for _, factory := range p.DataSources(context.Background()) {
		d := factory()
		var schemaResp datasource.SchemaResponse
		d.Schema(context.Background(), datasource.SchemaRequest{}, &schemaResp)
		if diags := schemaResp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
			t.Errorf("%T: %v", d, diags)
		}
	}
	var providerSchema provider.SchemaResponse
	p.Schema(context.Background(), provider.SchemaRequest{}, &providerSchema)
	if !providerSchema.Schema.Attributes["api_key"].IsSensitive() {
		t.Error("api_key must be sensitive")
	}
}

func TestProviderNeedsAPIKey(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	helper.UnitTest(t, helper.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []helper.TestStep{{
			Config: `
provider "reselling" {}
data "reselling_kubernetes_versions" "all" {}
`,
			ExpectError: regexpMust(`Missing API key`),
		}},
	})
}

func TestProviderReadsAPIKeyFromEnvironment(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET /products/kubernetes/versions", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{"versions": []any{map[string]any{"id": "v1", "version": "1.31", "is_default": true}}}
	})
	t.Setenv(APIKeyEnv, testAPIKey)
	t.Setenv(EndpointEnv, f.URL())

	helper.UnitTest(t, helper.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []helper.TestStep{
			{
				Config: `
provider "reselling" {}
data "reselling_kubernetes_versions" "all" {}
`,
				Check: helper.TestCheckResourceAttr("data.reselling_kubernetes_versions.all", "default_id", "v1"),
			},
		},
	})
}

func TestProviderRejectsBadTimeout(t *testing.T) {
	t.Setenv(APIKeyEnv, testAPIKey)
	helper.UnitTest(t, helper.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []helper.TestStep{{
			Config: `
provider "reselling" {
  timeout = "soon"
}
data "reselling_kubernetes_versions" "all" {}
`,
			ExpectError: regexpMust(`Invalid timeout`),
		}},
	})
}

func TestDefaultEndpointIsTheAPIHost(t *testing.T) {
	var resp provider.SchemaResponse
	New("test")().Schema(context.Background(), provider.SchemaRequest{}, &resp)
	desc := resp.Schema.Attributes["endpoint"].GetMarkdownDescription()
	if !strings.Contains(desc, "https://api.resellingprovider.de/api/v1") {
		t.Fatalf("endpoint description does not name the API host: %s", desc)
	}
}
