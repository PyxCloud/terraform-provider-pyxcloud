package provider

import (
	"context"
	"fmt"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type importDiscoveryDataSource struct {
	client client.Client
}

var (
	_ datasource.DataSource              = (*importDiscoveryDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*importDiscoveryDataSource)(nil)
)

func NewImportDiscoveryDataSource() datasource.DataSource {
	return &importDiscoveryDataSource{}
}

type importDiscoveryModel struct {
	AccountBinding    types.String   `tfsdk:"account_binding"`
	Cloud             types.String   `tfsdk:"cloud"`
	Region            types.String   `tfsdk:"region"`
	Filters           types.Map      `tfsdk:"filters"`
	ResourceTypes     []types.String `tfsdk:"resource_types"`
	Resources         types.String   `tfsdk:"resources"`
	ObservabilityOnly types.Bool     `tfsdk:"observability_only"`
}

func (d *importDiscoveryDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_import_discovery"
}

func (d *importDiscoveryDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Discovers importable resources from a backend-held PyxCloud account binding. " +
			"This data source is read-only and observability-only; it never accepts raw cloud credentials.",
		Attributes: map[string]schema.Attribute{
			"account_binding": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "PyxCloud account binding identifier. Raw cloud credentials are not accepted by this provider surface.",
			},
			"cloud": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional source cloud filter, e.g. `aws`, `gcp`, or `digitalocean`.",
			},
			"region": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional source cloud region filter.",
			},
			"filters": schema.MapAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Optional backend-defined discovery filters, such as tags.",
			},
			"resource_types": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Optional source resource type filters.",
			},
			"resources": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backend discovery result as compact JSON.",
			},
			"observability_only": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Always true for discovery; no migration/deployable topology fee is required.",
			},
		},
	}
}

func (d *importDiscoveryDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			fmt.Sprintf("expected *providerData, got %T", req.ProviderData),
		)
		return
	}
	d.client = pd.client
}

func (d *importDiscoveryDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data importDiscoveryModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	out, err := d.client.ImportDiscovery(ctx, client.ImportDiscoveryRequest{
		AccountBinding: data.AccountBinding.ValueString(),
		Cloud:          optionalString(data.Cloud),
		Region:         optionalString(data.Region),
		Filters:        stringMapFromTypes(data.Filters),
		ResourceTypes:  stringsFromTypeList(data.ResourceTypes),
	})
	if err != nil {
		resp.Diagnostics.AddError("Import discovery failed", err.Error())
		return
	}

	data.Resources = types.StringValue(out.ResourcesJSON())
	data.ObservabilityOnly = types.BoolValue(out.ObservabilityOnly)
	if !out.ObservabilityOnly {
		data.ObservabilityOnly = types.BoolValue(true)
		resp.Diagnostics.AddWarning(
			"Import discovery forced observability-only",
			"The backend did not mark discovery as observability-only. The provider surfaced the inventory as read-only and did not request deployable topology output.",
		)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func optionalString(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func stringsFromTypeList(values []types.String) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !v.IsNull() && !v.IsUnknown() && v.ValueString() != "" {
			out = append(out, v.ValueString())
		}
	}
	return out
}

func stringMapFromTypes(values types.Map) map[string]string {
	if values.IsNull() || values.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	for k, v := range values.Elements() {
		if s, ok := v.(types.String); ok && !s.IsNull() && !s.IsUnknown() {
			out[k] = s.ValueString()
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
