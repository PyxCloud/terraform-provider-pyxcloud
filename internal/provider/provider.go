// Package provider implements the PyxCloud Terraform provider on the modern
// terraform-plugin-framework. It exposes the PyxCloud canonical-topology
// abstraction to Terraform: a managed pyxcloud_topology resource and a
// pyxcloud_compare data source that prices a topology across providers/regions.
package provider

import (
	"context"
	"os"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// envToken is the environment fallback for the auth token.
const envToken = "PYXCLOUD_TOKEN"

// pyxCloudProvider is the framework provider implementation.
type pyxCloudProvider struct {
	version string
}

var _ provider.Provider = (*pyxCloudProvider)(nil)

// New returns a framework provider factory for the given build version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &pyxCloudProvider{version: version}
	}
}

func (p *pyxCloudProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "pyxcloud"
	resp.Version = p.version
}

// providerModel maps the provider configuration block.
type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Token    types.String `tfsdk:"token"`
}

func (p *pyxCloudProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Provider for the PyxCloud platform. Models the PyxCloud " +
			"canonical topology abstraction and prices it across cloud providers.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "PyxCloud API base URL. Defaults to `" +
					client.DefaultEndpoint + "`.",
			},
			"token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "OAuth/SSO-issued bearer token used to authenticate " +
					"against the PyxCloud API. Falls back to the `" + envToken +
					"` environment variable when unset.",
			},
		},
	}
}

func (p *pyxCloudProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := client.DefaultEndpoint
	if !cfg.Endpoint.IsNull() && cfg.Endpoint.ValueString() != "" {
		endpoint = cfg.Endpoint.ValueString()
	}

	token := os.Getenv(envToken)
	if !cfg.Token.IsNull() && cfg.Token.ValueString() != "" {
		token = cfg.Token.ValueString()
	}

	// MVP: the stub client requires no token, but warn so the config is realistic
	// for when the live HTTP client lands.
	if token == "" {
		resp.Diagnostics.AddWarning(
			"No PyxCloud token configured",
			"Set `token` or the "+envToken+" environment variable. The MVP stub "+
				"client does not require it, but the live API will.",
		)
	}

	c := client.NewStub(client.Config{Endpoint: endpoint, Token: token})
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *pyxCloudProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewTopologyResource,
	}
}

func (p *pyxCloudProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewCompareDataSource,
	}
}
