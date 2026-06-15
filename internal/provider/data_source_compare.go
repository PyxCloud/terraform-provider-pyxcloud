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
	Name       types.String     `tfsdk:"name"`
	Components []componentModel `tfsdk:"components"`
	Candidates []candidateModel `tfsdk:"candidates"`

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
			"components": schema.ListNestedAttribute{
				Required: true,
				MarkdownDescription: "Canonical components to price (same shape as the " +
					"pyxcloud_topology resource).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Required: true},
						"type": schema.StringAttribute{Required: true},
						"count": schema.Int64Attribute{
							Optional:            true,
							MarkdownDescription: "Instance count (defaults to 1).",
						},
						"vm": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"architecture": schema.StringAttribute{Optional: true},
								"cpu":          schema.StringAttribute{Optional: true},
								"ram":          schema.StringAttribute{Optional: true},
								"os_name":      schema.StringAttribute{Optional: true},
							},
						},
					},
				},
			},
			"candidates": schema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "(provider, region) targets to price the topology against.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"provider": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "`aws`, `gcp`, or `digitalocean`.",
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

	topo := client.Topology{Name: data.Name.ValueString()}
	topo.Components = make([]client.Component, 0, len(data.Components))
	for _, cm := range data.Components {
		count := int(cm.Count.ValueInt64())
		if count <= 0 {
			count = 1
		}
		comp := client.Component{
			Name:  cm.Name.ValueString(),
			Type:  cm.Type.ValueString(),
			Count: count,
		}
		if cm.VM != nil {
			comp.VM = &client.VMType{
				Architecture: cm.VM.Architecture.ValueString(),
				CPU:          cm.VM.CPU.ValueString(),
				RAM:          cm.VM.RAM.ValueString(),
				OS:           cm.VM.OS.ValueString(),
			}
		}
		topo.Components = append(topo.Components, comp)
	}

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

func costToModel(c client.CandidateCost) candidateCostModel {
	return candidateCostModel{
		Provider:   types.StringValue(c.Provider),
		Region:     types.StringValue(c.Region),
		HourlyUSD:  types.Float64Value(c.HourlyUSD),
		MonthlyUSD: types.Float64Value(c.MonthlyUSD),
		Priceable:  types.BoolValue(c.Priceable),
	}
}
