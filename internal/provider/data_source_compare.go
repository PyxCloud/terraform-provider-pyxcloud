package provider

import (
	"context"
	"fmt"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// compareDataSource prices a canonical topology across candidate
// (provider, region) pairs — the Terraform analogue of the console Compare page.
type compareDataSource struct {
	client client.Client
}

var (
	_ datasource.DataSource              = (*compareDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*compareDataSource)(nil)
)

// NewCompareDataSource is the framework data-source factory.
func NewCompareDataSource() datasource.DataSource {
	return &compareDataSource{}
}

// candidateModel is one (provider, region) target to price against.
type candidateModel struct {
	Provider types.String `tfsdk:"provider"`
	Region   types.String `tfsdk:"region"`
}

// candidateCostModel is the priced result for one candidate.
type candidateCostModel struct {
	Provider   types.String  `tfsdk:"provider"`
	Region     types.String  `tfsdk:"region"`
	HourlyUSD  types.Float64 `tfsdk:"hourly_usd"`
	MonthlyUSD types.Float64 `tfsdk:"monthly_usd"`
	Priceable  types.Bool    `tfsdk:"priceable"`
}

// compareModel maps the pyxcloud_compare data source.
type compareModel struct {
	// Inputs: an inline canonical topology to price + candidate targets.
	Name                            types.String     `tfsdk:"name"`
	PyxVPC                          []componentModel `tfsdk:"pyx_vpc"`
	PyxNetworkRule                  []componentModel `tfsdk:"pyx_network_rule"`
	PyxAccessPolicy                 []componentModel `tfsdk:"pyx_access_policy"`
	PyxMonitoring                   []componentModel `tfsdk:"pyx_monitoring"`
	PyxDNS                          []componentModel `tfsdk:"pyx_dns"`
	PyxVirtualMachine               []componentModel `tfsdk:"pyx_virtual_machine"`
	PyxAutoscaleVirtualMachineGroup []componentModel `tfsdk:"pyx_autoscale_virtual_machine_group"`
	PyxDatabase                     []componentModel `tfsdk:"pyx_database"`
	PyxLoadBalancer                 []componentModel `tfsdk:"pyx_load_balancer"`
	PyxCache                        []componentModel `tfsdk:"pyx_cache"`
	PyxObjectStorage                []componentModel `tfsdk:"pyx_object_storage"`
	PyxSecret                       []componentModel `tfsdk:"pyx_secret"`
	PyxQueue                        []componentModel `tfsdk:"pyx_queue"`
	PyxStream                       []componentModel `tfsdk:"pyx_stream"`
	PyxServerlessFunction           []componentModel `tfsdk:"pyx_serverless_function"`
	PyxWebService                   []componentModel `tfsdk:"pyx_web_service"`
	PyxKMS                          []componentModel `tfsdk:"pyx_kms"`
	PyxCDN                          []componentModel `tfsdk:"pyx_cdn"`
	PyxWAF                          []componentModel `tfsdk:"pyx_waf"`
	PyxKubernetes                   []componentModel `tfsdk:"pyx_kubernetes"`
	PyxEmail                        []componentModel `tfsdk:"pyx_email"`
	PyxBlockStorage                 []componentModel `tfsdk:"pyx_block_storage"`
	PyxPrefixList                   []componentModel `tfsdk:"pyx_prefix_list"`
	PyxSynthetics                   []componentModel `tfsdk:"pyx_synthetics"`
	PyxALBAttachment                []componentModel `tfsdk:"pyx_alb_attachment"`
	Candidates                      []candidateModel `tfsdk:"candidates"`

	// Outputs.
	Results  []candidateCostModel `tfsdk:"results"`
	Cheapest *candidateCostModel  `tfsdk:"cheapest"`
}

func (d *compareDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_compare"
}

func (d *compareDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Prices a PyxCloud canonical topology across a set of " +
			"candidate (provider, region) targets and returns per-candidate cost, " +
			"cheapest first — mirroring the console Compare page.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional label for the topology being compared.",
			},
			"pyx_vpc":                             pyxCompareComponentBlock("PyxCloud VPC/network component."),
			"pyx_network_rule":                    pyxCompareComponentBlock("PyxCloud network rule component."),
			"pyx_access_policy":                   pyxCompareComponentBlock("PyxCloud access policy component."),
			"pyx_monitoring":                      pyxCompareComponentBlock("PyxCloud monitoring component."),
			"pyx_dns":                             pyxCompareComponentBlock("PyxCloud DNS component."),
			"pyx_virtual_machine":                 pyxCompareComponentBlock("PyxCloud virtual machine component."),
			"pyx_autoscale_virtual_machine_group": pyxCompareComponentBlock("PyxCloud autoscaling virtual machine group component."),
			"pyx_database":                        pyxCompareComponentBlock("PyxCloud managed database component."),
			"pyx_load_balancer":                   pyxCompareComponentBlock("PyxCloud load balancer component."),
			"pyx_cache":                           pyxCompareComponentBlock("PyxCloud cache component."),
			"pyx_object_storage":                  pyxCompareComponentBlock("PyxCloud object storage component."),
			"pyx_secret":                          pyxCompareComponentBlock("PyxCloud secret manager component."),
			"pyx_queue":                           pyxCompareComponentBlock("PyxCloud queue component."),
			"pyx_stream":                          pyxCompareComponentBlock("PyxCloud stream component."),
			"pyx_serverless_function":             pyxCompareComponentBlock("PyxCloud serverless function component."),
			"pyx_web_service":                     pyxCompareComponentBlock("PyxCloud always-on web service (DO App Platform service)."),
			"pyx_kms":                             pyxCompareComponentBlock("PyxCloud KMS/encryption-key component."),
			"pyx_cdn":                             pyxCompareComponentBlock("PyxCloud CDN component."),
			"pyx_waf":                             pyxCompareComponentBlock("PyxCloud WAF component."),
			"pyx_kubernetes":                      pyxCompareComponentBlock("PyxCloud Kubernetes component."),
			"pyx_email":                           pyxCompareComponentBlock("PyxCloud email component."),
			"pyx_block_storage":                   pyxCompareComponentBlock("PyxCloud block storage component."),
			"pyx_prefix_list":                     pyxCompareComponentBlock("PyxCloud prefix list component."),
			"pyx_synthetics":                      pyxCompareComponentBlock("PyxCloud synthetics component."),
			"pyx_alb_attachment":                  pyxCompareComponentBlock("PyxCloud existing ALB attachment component."),
			"candidates": schema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "(provider, region) targets to price the topology against.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"provider": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "`aws`, `gcp`, `digitalocean`, or `ovh` (wave-2).",
						},
						"region": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Abstract macro-region, e.g. `EU West`.",
						},
					},
				},
			},
			"results": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Per-candidate cost, cheapest first.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"provider":    schema.StringAttribute{Computed: true},
						"region":      schema.StringAttribute{Computed: true},
						"hourly_usd":  schema.Float64Attribute{Computed: true},
						"monthly_usd": schema.Float64Attribute{Computed: true},
						"priceable": schema.BoolAttribute{
							Computed:            true,
							MarkdownDescription: "False when no complete price match exists.",
						},
					},
				},
			},
			"cheapest": schema.SingleNestedAttribute{
				Computed:            true,
				MarkdownDescription: "The cheapest priceable candidate, if any.",
				Attributes: map[string]schema.Attribute{
					"provider":    schema.StringAttribute{Computed: true},
					"region":      schema.StringAttribute{Computed: true},
					"hourly_usd":  schema.Float64Attribute{Computed: true},
					"monthly_usd": schema.Float64Attribute{Computed: true},
					"priceable":   schema.BoolAttribute{Computed: true},
				},
			},
		},
	}
}

func pyxCompareComponentBlock(description string) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional:            true,
		MarkdownDescription: description + " Properties are flat at the `pyx_*` block level.",
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"path": schema.StringAttribute{
					Optional:            true,
					MarkdownDescription: "Canonical topology path for this component, e.g. `/0/Europe/0/Web-Net/0/app`.",
				},
				"name":         schema.StringAttribute{Required: true},
				"count":        schema.Int64Attribute{Optional: true, MarkdownDescription: "Instance count (defaults to 1)."},
				"architecture": schema.StringAttribute{Optional: true},
				"cpu":          schema.StringAttribute{Optional: true},
				"ram":          schema.StringAttribute{Optional: true},
				"os_name":      schema.StringAttribute{Optional: true},
				"min":          schema.Int64Attribute{Optional: true},
				"max":          schema.Int64Attribute{Optional: true},
				"desired":      schema.Int64Attribute{Optional: true},
				"health":       schema.StringAttribute{Optional: true},
			},
		},
	}
}

func (d *compareDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *compareDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data compareModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	topo := client.Topology{Name: data.Name.ValueString(), Components: compareComponentsFromModel(data)}

	candidates := make([]client.Candidate, 0, len(data.Candidates))
	for _, cm := range data.Candidates {
		candidates = append(candidates, client.Candidate{
			Provider: cm.Provider.ValueString(),
			Region:   cm.Region.ValueString(),
		})
	}

	costs, err := d.client.Compare(ctx, topo, candidates)
	if err != nil {
		resp.Diagnostics.AddError("Compare failed", err.Error())
		return
	}

	data.Results = make([]candidateCostModel, 0, len(costs))
	for _, c := range costs {
		data.Results = append(data.Results, costToModel(c))
	}
	// costs are already cheapest-first; pick the first priceable as cheapest.
	data.Cheapest = nil
	for _, c := range costs {
		if c.Priceable {
			m := costToModel(c)
			data.Cheapest = &m
			break
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func compareComponentsFromModel(m compareModel) []client.Component {
	var comps []client.Component
	appendComponents := func(canonicalType string, models []componentModel) {
		for _, cm := range models {
			comps = append(comps, componentModelToClient(canonicalType, cm))
		}
	}
	appendComponents("vpc", m.PyxVPC)
	appendComponents("network-rule", m.PyxNetworkRule)
	appendComponents("access-policy", m.PyxAccessPolicy)
	appendComponents("monitoring", m.PyxMonitoring)
	appendComponents("dns", m.PyxDNS)
	appendComponents("virtual-machine", m.PyxVirtualMachine)
	appendComponents("virtual-machine-scale-group", m.PyxAutoscaleVirtualMachineGroup)
	appendComponents("managed-database", m.PyxDatabase)
	appendComponents("load-balancer", m.PyxLoadBalancer)
	appendComponents("cache", m.PyxCache)
	appendComponents("object-storage", m.PyxObjectStorage)
	appendComponents("secrets-manager", m.PyxSecret)
	appendComponents("managed-queue", m.PyxQueue)
	appendComponents("event-streaming", m.PyxStream)
	appendComponents("serverless-function", m.PyxServerlessFunction)
	appendComponents("web-service", m.PyxWebService)
	appendComponents("kms", m.PyxKMS)
	appendComponents("cdn", m.PyxCDN)
	appendComponents("waf", m.PyxWAF)
	appendComponents("kubernetes", m.PyxKubernetes)
	appendComponents("email", m.PyxEmail)
	appendComponents("block-storage", m.PyxBlockStorage)
	appendComponents("prefix-list", m.PyxPrefixList)
	appendComponents("synthetics", m.PyxSynthetics)
	appendComponents("attach-to-existing-alb", m.PyxALBAttachment)
	return comps
}

func costToModel(c client.CandidateCost) candidateCostModel {
	return candidateCostModel{
		Provider:   types.StringValue(c.Provider),
		Region:     types.StringValue(c.Region),
		HourlyUSD:  types.Float64Value(c.HourlyUSD),
		MonthlyUSD: types.Float64Value(c.MonthlyUSD),
		Priceable:  types.BoolValue(c.Priceable),
	}
}
