package provider

import (
	"context"
	"fmt"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// topologyResource manages a PyxCloud canonical topology.
type topologyResource struct {
	client  client.Client
	catalog catalog.RegionCatalog
}

var (
	_ resource.Resource               = (*topologyResource)(nil)
	_ resource.ResourceWithConfigure  = (*topologyResource)(nil)
	_ resource.ResourceWithModifyPlan = (*topologyResource)(nil)
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

// networkModel maps the abstract `network` block of a place: the canonical
// place { region; cidr; subnets } network description (pd-TF-REGION-VPC).
type networkModel struct {
	CIDR    types.String   `tfsdk:"cidr"`
	Subnets []types.String `tfsdk:"subnets"`
}

// subnetPlanModel is one concrete subnet in the resolved network plan.
type subnetPlanModel struct {
	Name types.String `tfsdk:"name"`
	CIDR types.String `tfsdk:"cidr"`
	Zone types.String `tfsdk:"zone"`
}

// networkPlanModel is the computed, catalog-resolved concrete network plan
// (the abstract→concrete translation surfaced back into state).
type networkPlanModel struct {
	Provider     types.String      `tfsdk:"provider"`
	CSP          types.String      `tfsdk:"csp"`
	RegionName   types.String      `tfsdk:"region_name"`
	CSPRegion    types.String      `tfsdk:"csp_region"`
	VPCName      types.String      `tfsdk:"vpc_name"`
	CIDR         types.String      `tfsdk:"cidr"`
	ResourceType types.String      `tfsdk:"resource_type"`
	Subnets      []subnetPlanModel `tfsdk:"subnets"`
}

// securityRuleModel maps one abstract ingress/egress rule (pd-TF-SG): a port
// range + protocol scoped to either CIDRs or a peer security-group reference.
type securityRuleModel struct {
	Direction types.String   `tfsdk:"direction"`
	Protocol  types.String   `tfsdk:"protocol"`
	FromPort  types.Int64    `tfsdk:"from_port"`
	ToPort    types.Int64    `tfsdk:"to_port"`
	CIDRs     []types.String `tfsdk:"cidrs"`
	SourceSG  types.String   `tfsdk:"source_sg"`
}

// securityGroupModel maps the abstract `security_group` block of a place: the
// canonical `expose` shorthand plus explicit ingress/egress rules, attached to
// the place's network (pd-TF-SG).
type securityGroupModel struct {
	Name        types.String        `tfsdk:"name"`
	Description types.String        `tfsdk:"description"`
	Expose      []types.Int64       `tfsdk:"expose"`
	Rules       []securityRuleModel `tfsdk:"rules"`
}

// rulePlanModel is one concrete, resolved rule in the security-group plan.
type rulePlanModel struct {
	Direction types.String   `tfsdk:"direction"`
	Protocol  types.String   `tfsdk:"protocol"`
	FromPort  types.Int64    `tfsdk:"from_port"`
	ToPort    types.Int64    `tfsdk:"to_port"`
	CIDRs     []types.String `tfsdk:"cidrs"`
	SourceSG  types.String   `tfsdk:"source_sg"`
}

// securityGroupPlanModel is the computed, catalog-resolved concrete
// security-group/firewall plan (the abstract→concrete translation in state).
type securityGroupPlanModel struct {
	Provider     types.String    `tfsdk:"provider"`
	CSP          types.String    `tfsdk:"csp"`
	RegionName   types.String    `tfsdk:"region_name"`
	CSPRegion    types.String    `tfsdk:"csp_region"`
	SGName       types.String    `tfsdk:"sg_name"`
	NetworkName  types.String    `tfsdk:"network_name"`
	Description  types.String    `tfsdk:"description"`
	ResourceType types.String    `tfsdk:"resource_type"`
	Rules        []rulePlanModel `tfsdk:"rules"`
}

// topologyModel maps the pyxcloud_topology resource state.
type topologyModel struct {
	ID          types.String      `tfsdk:"id"`
	Name        types.String      `tfsdk:"name"`
	Provider    types.String      `tfsdk:"provider"`
	Region      types.String      `tfsdk:"region"`
	Components        []componentModel        `tfsdk:"components"`
	Network           *networkModel           `tfsdk:"network"`
	NetworkPlan       *networkPlanModel       `tfsdk:"network_plan"`
	SecurityGroup     *securityGroupModel     `tfsdk:"security_group"`
	SecurityGroupPlan *securityGroupPlanModel `tfsdk:"security_group_plan"`
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
			"network": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Abstract network for the place (pd-TF-REGION-VPC): a " +
					"provider-neutral VPC CIDR + subnet CIDRs. Resolved to a concrete VPC/" +
					"network and multi-AZ subnets via the region catalog at plan time.",
				Attributes: map[string]schema.Attribute{
					"cidr": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "VPC/network CIDR, e.g. `10.0.0.0/16`.",
					},
					"subnets": schema.ListAttribute{
						Optional:    true,
						ElementType: types.StringType,
						MarkdownDescription: "Subnet CIDRs (must be inside `cidr`). For AWS/GCP " +
							"each subnet is placed in a distinct availability zone derived from " +
							"the resolved concrete region; DigitalOcean VPCs are region-scoped.",
					},
				},
			},
			"network_plan": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "Computed concrete network plan: the catalog-resolved " +
					"translation of the abstract `network` for the topology's provider. The " +
					"`csp_region` is resolved from the catalog (never invented).",
				Attributes: map[string]schema.Attribute{
					"provider":      schema.StringAttribute{Computed: true},
					"csp":           schema.StringAttribute{Computed: true},
					"region_name":   schema.StringAttribute{Computed: true},
					"csp_region":    schema.StringAttribute{Computed: true},
					"vpc_name":      schema.StringAttribute{Computed: true},
					"cidr":          schema.StringAttribute{Computed: true},
					"resource_type": schema.StringAttribute{Computed: true},
					"subnets": schema.ListNestedAttribute{
						Computed: true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{Computed: true},
								"cidr": schema.StringAttribute{Computed: true},
								"zone": schema.StringAttribute{Computed: true},
							},
						},
					},
				},
			},
			"security_group": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Abstract security-group/firewall for the place " +
					"(pd-TF-SG): a canonical `expose` port shorthand plus explicit " +
					"ingress/egress rules, attached to the place's `network`. Resolved to " +
					"`aws_security_group(_rule)` / `google_compute_firewall` / " +
					"`digitalocean_firewall` for the topology's provider at plan time.",
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Security-group name; defaults to the topology name.",
					},
					"description": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Human description. Sanitised to ASCII at plan " +
							"time — AWS rejects non-ASCII security-group descriptions.",
					},
					"expose": schema.ListAttribute{
						Optional:    true,
						ElementType: types.Int64Type,
						MarkdownDescription: "Canonical shorthand: each TCP port opened " +
							"ingress from `0.0.0.0/0` + `::/0`, e.g. `[80, 443]`.",
					},
					"rules": schema.ListNestedAttribute{
						Optional: true,
						MarkdownDescription: "Explicit ingress/egress rules layered on top " +
							"of `expose`.",
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"direction": schema.StringAttribute{
									Required:            true,
									MarkdownDescription: "`ingress` or `egress`.",
								},
								"protocol": schema.StringAttribute{
									Required:            true,
									MarkdownDescription: "`tcp`, `udp`, `icmp`, or `all`.",
								},
								"from_port": schema.Int64Attribute{
									Optional:            true,
									MarkdownDescription: "Inclusive low port (omit for icmp/all).",
								},
								"to_port": schema.Int64Attribute{
									Optional:            true,
									MarkdownDescription: "Inclusive high port (omit for icmp/all).",
								},
								"cidrs": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									MarkdownDescription: "Source (ingress) / destination " +
										"(egress) CIDRs. Mutually exclusive with `source_sg`.",
								},
								"source_sg": schema.StringAttribute{
									Optional: true,
									MarkdownDescription: "Canonical name of a peer " +
										"security-group. Mutually exclusive with `cidrs`.",
								},
							},
						},
					},
				},
			},
			"security_group_plan": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "Computed concrete security-group/firewall plan: the " +
					"catalog-resolved translation of the abstract `security_group` for the " +
					"topology's provider. The `description` is ASCII-sanitised.",
				Attributes: map[string]schema.Attribute{
					"provider":      schema.StringAttribute{Computed: true},
					"csp":           schema.StringAttribute{Computed: true},
					"region_name":   schema.StringAttribute{Computed: true},
					"csp_region":    schema.StringAttribute{Computed: true},
					"sg_name":       schema.StringAttribute{Computed: true},
					"network_name":  schema.StringAttribute{Computed: true},
					"description":   schema.StringAttribute{Computed: true},
					"resource_type": schema.StringAttribute{Computed: true},
					"rules": schema.ListNestedAttribute{
						Computed: true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"direction": schema.StringAttribute{Computed: true},
								"protocol":  schema.StringAttribute{Computed: true},
								"from_port": schema.Int64Attribute{Computed: true},
								"to_port":   schema.Int64Attribute{Computed: true},
								"cidrs": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
								},
								"source_sg": schema.StringAttribute{Computed: true},
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
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data",
			fmt.Sprintf("expected *providerData, got %T", req.ProviderData),
		)
		return
	}
	r.client = pd.client
	r.catalog = pd.catalog
}

// ModifyPlan resolves the abstract network against the catalog at plan time so
// that a missing/unavailable region surfaces as a clear plan-time error (never
// a silent fallback or an apply-time surprise), per SPEC §4.
func (r *topologyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // resource is being destroyed
	}
	var plan topologyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.catalog == nil {
		return
	}
	if plan.Network != nil {
		if _, err := r.translateNetwork(ctx, plan); err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("region"),
				"Network region not resolvable from the PyxCloud catalog",
				err.Error(),
			)
		}
	}
	if plan.SecurityGroup != nil {
		if _, err := r.translateSecurityGroup(ctx, plan); err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("security_group"),
				"Security-group not resolvable / invalid against the PyxCloud catalog",
				err.Error(),
			)
		}
	}
}

func (r *topologyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan topologyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	netPlan, err := r.translateNetwork(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Network translation failed", err.Error())
		return
	}
	sgPlan, err := r.translateSecurityGroup(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Security-group translation failed", err.Error())
		return
	}

	created, err := r.client.CreateTopology(ctx, modelToTopology(plan))
	if err != nil {
		resp.Diagnostics.AddError("Create topology failed", err.Error())
		return
	}

	state := topologyToModel(created)
	state.Network = plan.Network
	state.NetworkPlan = netPlan
	state.SecurityGroup = plan.SecurityGroup
	state.SecurityGroupPlan = sgPlan
	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

// translateNetwork resolves the abstract network block into a concrete plan via
// the catalog. Returns (nil, nil) when the topology declares no network.
func (r *topologyResource) translateNetwork(ctx context.Context, m topologyModel) (*networkPlanModel, error) {
	if m.Network == nil {
		return nil, nil
	}
	subnets := make([]string, 0, len(m.Network.Subnets))
	for _, s := range m.Network.Subnets {
		subnets = append(subnets, s.ValueString())
	}
	spec := catalog.NetworkSpec{
		Name:     m.Name.ValueString(),
		Region:   m.Region.ValueString(),
		Provider: m.Provider.ValueString(),
		CIDR:     m.Network.CIDR.ValueString(),
		Subnets:  subnets,
	}
	cp, err := catalog.TranslateNetwork(ctx, r.catalog, spec)
	if err != nil {
		return nil, err
	}
	out := &networkPlanModel{
		Provider:     types.StringValue(cp.Provider),
		CSP:          types.StringValue(cp.CSP),
		RegionName:   types.StringValue(cp.RegionName),
		CSPRegion:    types.StringValue(cp.CSPRegion),
		VPCName:      types.StringValue(cp.VPCName),
		CIDR:         types.StringValue(cp.CIDR),
		ResourceType: types.StringValue(cp.ResourceType),
	}
	for _, sp := range cp.Subnets {
		out.Subnets = append(out.Subnets, subnetPlanModel{
			Name: types.StringValue(sp.Name),
			CIDR: types.StringValue(sp.CIDR),
			Zone: types.StringValue(sp.Zone),
		})
	}
	return out, nil
}

// translateSecurityGroup resolves the abstract security_group block into a
// concrete plan via the catalog. Returns (nil, nil) when none is declared.
func (r *topologyResource) translateSecurityGroup(ctx context.Context, m topologyModel) (*securityGroupPlanModel, error) {
	if m.SecurityGroup == nil {
		return nil, nil
	}
	sg := m.SecurityGroup

	expose := make([]int, 0, len(sg.Expose))
	for _, p := range sg.Expose {
		expose = append(expose, int(p.ValueInt64()))
	}
	rules := make([]catalog.SecurityRule, 0, len(sg.Rules))
	for _, rm := range sg.Rules {
		cidrs := make([]string, 0, len(rm.CIDRs))
		for _, c := range rm.CIDRs {
			cidrs = append(cidrs, c.ValueString())
		}
		rules = append(rules, catalog.SecurityRule{
			Direction: rm.Direction.ValueString(),
			Protocol:  rm.Protocol.ValueString(),
			FromPort:  int(rm.FromPort.ValueInt64()),
			ToPort:    int(rm.ToPort.ValueInt64()),
			CIDRs:     cidrs,
			SourceSG:  rm.SourceSG.ValueString(),
		})
	}

	name := sg.Name.ValueString()
	if name == "" {
		name = m.Name.ValueString()
	}
	network := ""
	if m.Network != nil {
		network = m.Name.ValueString()
	}

	spec := catalog.SecurityGroupSpec{
		Name:        name,
		Network:     network,
		Region:      m.Region.ValueString(),
		Provider:    m.Provider.ValueString(),
		Description: sg.Description.ValueString(),
		Expose:      expose,
		Rules:       rules,
	}
	cp, err := catalog.TranslateSecurityGroup(ctx, r.catalog, spec)
	if err != nil {
		return nil, err
	}

	out := &securityGroupPlanModel{
		Provider:     types.StringValue(cp.Provider),
		CSP:          types.StringValue(cp.CSP),
		RegionName:   types.StringValue(cp.RegionName),
		CSPRegion:    types.StringValue(cp.CSPRegion),
		SGName:       types.StringValue(cp.SGName),
		NetworkName:  types.StringValue(cp.NetworkName),
		Description:  types.StringValue(cp.Description),
		ResourceType: types.StringValue(cp.ResourceType),
	}
	for _, rp := range cp.Rules {
		cidrs := make([]types.String, 0, len(rp.CIDRs))
		for _, c := range rp.CIDRs {
			cidrs = append(cidrs, types.StringValue(c))
		}
		out.Rules = append(out.Rules, rulePlanModel{
			Direction: types.StringValue(rp.Direction),
			Protocol:  types.StringValue(rp.Protocol),
			FromPort:  types.Int64Value(int64(rp.FromPort)),
			ToPort:    types.Int64Value(int64(rp.ToPort)),
			CIDRs:     cidrs,
			SourceSG:  types.StringValue(rp.SourceSG),
		})
	}
	return out, nil
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

	refreshed := topologyToModel(got)
	// Preserve the abstract network input and re-derive the concrete plan so
	// drift in the catalog (e.g. a region gaining/losing a provider) is caught.
	refreshed.Network = state.Network
	if refreshed.Network != nil {
		netPlan, terr := r.translateNetwork(ctx, refreshed)
		if terr != nil {
			resp.Diagnostics.AddError("Network translation failed", terr.Error())
			return
		}
		refreshed.NetworkPlan = netPlan
	}
	refreshed.SecurityGroup = state.SecurityGroup
	if refreshed.SecurityGroup != nil {
		sgPlan, terr := r.translateSecurityGroup(ctx, refreshed)
		if terr != nil {
			resp.Diagnostics.AddError("Security-group translation failed", terr.Error())
			return
		}
		refreshed.SecurityGroupPlan = sgPlan
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, refreshed)...)
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

	netPlan, err := r.translateNetwork(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Network translation failed", err.Error())
		return
	}
	sgPlan, err := r.translateSecurityGroup(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Security-group translation failed", err.Error())
		return
	}

	updated, err := r.client.UpdateTopology(ctx, desired)
	if err != nil {
		resp.Diagnostics.AddError("Update topology failed", err.Error())
		return
	}

	newState := topologyToModel(updated)
	newState.Network = plan.Network
	newState.NetworkPlan = netPlan
	newState.SecurityGroup = plan.SecurityGroup
	newState.SecurityGroupPlan = sgPlan
	resp.Diagnostics.Append(resp.State.Set(ctx, newState)...)
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
