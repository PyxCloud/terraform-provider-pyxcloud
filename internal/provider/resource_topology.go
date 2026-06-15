package provider

import (
	"context"
	"fmt"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// topologyResource manages a PyxCloud canonical topology.
type topologyResource struct {
	client client.Client
}

var (
	_ resource.Resource              = (*topologyResource)(nil)
	_ resource.ResourceWithConfigure = (*topologyResource)(nil)
)

// NewTopologyResource is the framework resource factory.
func NewTopologyResource() resource.Resource {
	return &topologyResource{}
}

// vmTypeModel maps the virtual-machine sizing block, mirroring the canonical
// properties.virtual-machine.type.* / os.osName shape.
type vmTypeModel struct {
	Architecture types.String `tfsdk:"architecture"`
	CPU          types.String `tfsdk:"cpu"`
	RAM          types.String `tfsdk:"ram"`
	OS           types.String `tfsdk:"os_name"`
}

// componentModel maps one canonical topology component.
type componentModel struct {
	Name  types.String `tfsdk:"name"`
	Type  types.String `tfsdk:"type"`
	Count types.Int64  `tfsdk:"count"`
	VM    *vmTypeModel `tfsdk:"vm"`
}

// topologyModel maps the pyxcloud_topology resource state.
type topologyModel struct {
	ID         types.String     `tfsdk:"id"`
	Name       types.String     `tfsdk:"name"`
	Provider   types.String     `tfsdk:"provider"`
	Region     types.String     `tfsdk:"region"`
	Components []componentModel `tfsdk:"components"`
}

func (r *topologyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_topology"
}

func (r *topologyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A PyxCloud canonical topology: a provider-independent " +
			"description of an infrastructure stack (components + sizing) pinned to a " +
			"deployment provider and macro-region.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Server-assigned topology identifier.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable topology name.",
			},
			"provider": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Deployment provider: `aws`, `gcp`, or " +
					"`digitalocean` (PyxCloud enabled launch providers).",
			},
			"region": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Abstract PyxCloud macro-region, e.g. `EU West`, " +
					"`US East`, `Asia` — resolved to a concrete CSP region at deploy time.",
			},
			"components": schema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "Canonical components that make up the topology.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Required:            true,
							MarkdownDescription: "Component name, unique within the topology.",
						},
						"type": schema.StringAttribute{
							Required: true,
							MarkdownDescription: "Canonical component type, e.g. " +
								"`virtual-machine`, `virtual-machine-scale-group`, " +
								"`managed-database`, `load-balancer`, `cache`, " +
								"`object-storage`, `blob-storage`.",
						},
						"count": schema.Int64Attribute{
							Optional: true,
							Computed: true,
							MarkdownDescription: "Number of instances of this component " +
								"(defaults to 1).",
						},
						"vm": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Sizing for virtual-machine components.",
							Attributes: map[string]schema.Attribute{
								"architecture": schema.StringAttribute{
									Optional:            true,
									MarkdownDescription: "CPU architecture, e.g. `x86_64`, `arm64`.",
								},
								"cpu": schema.StringAttribute{
									Optional:            true,
									MarkdownDescription: "vCPU count, e.g. `2`.",
								},
								"ram": schema.StringAttribute{
									Optional:            true,
									MarkdownDescription: "RAM in GiB, e.g. `4`.",
								},
								"os_name": schema.StringAttribute{
									Optional:            true,
									MarkdownDescription: "Operating system, e.g. `ubuntu`.",
								},
							},
						},
					},
				},
			},
		},
	}
}

func (r *topologyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			fmt.Sprintf("expected client.Client, got %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

func (r *topologyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan topologyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	created, err := r.client.CreateTopology(ctx, modelToTopology(plan))
	if err != nil {
		resp.Diagnostics.AddError("Create topology failed", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, topologyToModel(created))...)
}

func (r *topologyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state topologyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, found, err := r.client.GetTopology(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Read topology failed", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, topologyToModel(got))...)
}

func (r *topologyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan topologyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state topologyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	desired := modelToTopology(plan)
	desired.ID = state.ID.ValueString()

	updated, err := r.client.UpdateTopology(ctx, desired)
	if err != nil {
		resp.Diagnostics.AddError("Update topology failed", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, topologyToModel(updated))...)
}

func (r *topologyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state topologyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteTopology(ctx, state.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Delete topology failed", err.Error())
	}
}

// modelToTopology converts Terraform state into the canonical client model.
func modelToTopology(m topologyModel) client.Topology {
	comps := make([]client.Component, 0, len(m.Components))
	for _, cm := range m.Components {
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
		comps = append(comps, comp)
	}
	return client.Topology{
		ID:         m.ID.ValueString(),
		Name:       m.Name.ValueString(),
		Provider:   m.Provider.ValueString(),
		Region:     m.Region.ValueString(),
		Components: comps,
	}
}

// topologyToModel converts the canonical client model back into Terraform state.
func topologyToModel(t client.Topology) topologyModel {
	comps := make([]componentModel, 0, len(t.Components))
	for _, c := range t.Components {
		cm := componentModel{
			Name:  types.StringValue(c.Name),
			Type:  types.StringValue(c.Type),
			Count: types.Int64Value(int64(c.Count)),
		}
		if c.VM != nil {
			cm.VM = &vmTypeModel{
				Architecture: types.StringValue(c.VM.Architecture),
				CPU:          types.StringValue(c.VM.CPU),
				RAM:          types.StringValue(c.VM.RAM),
				OS:           types.StringValue(c.VM.OS),
			}
		}
		comps = append(comps, cm)
	}
	return topologyModel{
		ID:         types.StringValue(t.ID),
		Name:       types.StringValue(t.Name),
		Provider:   types.StringValue(t.Provider),
		Region:     types.StringValue(t.Region),
		Components: comps,
	}
}
