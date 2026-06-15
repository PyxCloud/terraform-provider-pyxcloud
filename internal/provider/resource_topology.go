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
	catalog catalog.VMCatalog
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

// virtualMachineModel maps the abstract `virtual_machine` block of a place: the
// canonical sizing (architecture, cpu, ram, os) + count, placed in the place's
// network and attached to its security-group (pd-TF-EC2-VM).
type virtualMachineModel struct {
	Name         types.String `tfsdk:"name"`
	Architecture types.String `tfsdk:"architecture"`
	CPU          types.Int64  `tfsdk:"cpu"`
	RAM          types.Int64  `tfsdk:"ram"`
	OS           types.String `tfsdk:"os"`
	OSVersion    types.String `tfsdk:"os_version"`
	Count        types.Int64  `tfsdk:"count"`
}

// vmInstancePlanModel is one concrete instance in the resolved VM plan.
type vmInstancePlanModel struct {
	Name types.String `tfsdk:"name"`
}

// virtualMachinePlanModel is the computed, catalog-resolved concrete VM plan
// (the abstract→concrete instance-type + image translation in state).
type virtualMachinePlanModel struct {
	Provider      types.String          `tfsdk:"provider"`
	CSP           types.String          `tfsdk:"csp"`
	RegionName    types.String          `tfsdk:"region_name"`
	CSPRegion     types.String          `tfsdk:"csp_region"`
	VMName        types.String          `tfsdk:"vm_name"`
	InstanceType  types.String          `tfsdk:"instance_type"`
	Architecture  types.String          `tfsdk:"architecture"`
	CPU           types.Int64           `tfsdk:"cpu"`
	RAM           types.Int64           `tfsdk:"ram"`
	OSName        types.String          `tfsdk:"os_name"`
	OSVersion     types.String          `tfsdk:"os_version"`
	Image         types.String          `tfsdk:"image"`
	NetworkName   types.String          `tfsdk:"network_name"`
	SubnetName    types.String          `tfsdk:"subnet_name"`
	SecurityGroup types.String          `tfsdk:"security_group"`
	ResourceType  types.String          `tfsdk:"resource_type"`
	Instances     []vmInstancePlanModel `tfsdk:"instances"`
}

// scaleGroupModel maps the abstract `scale_group` block of a place: canonical
// sizing (architecture, cpu, ram, os) + autoscale bounds (min/max/desired) +
// health, placed in the place's network (spread multi-AZ across its subnets) and
// attached to its security-group (pd-TF-ASG).
type scaleGroupModel struct {
	Name         types.String `tfsdk:"name"`
	Architecture types.String `tfsdk:"architecture"`
	CPU          types.Int64  `tfsdk:"cpu"`
	RAM          types.Int64  `tfsdk:"ram"`
	OS           types.String `tfsdk:"os"`
	OSVersion    types.String `tfsdk:"os_version"`
	Min          types.Int64  `tfsdk:"min"`
	Max          types.Int64  `tfsdk:"max"`
	Desired      types.Int64  `tfsdk:"desired"`
	Health       types.String `tfsdk:"health"`
}

// scaleGroupPlanModel is the computed, catalog-resolved concrete scale-group plan
// (the abstract→concrete launch-template + autoscaling-group translation).
type scaleGroupPlanModel struct {
	Provider      types.String   `tfsdk:"provider"`
	CSP           types.String   `tfsdk:"csp"`
	RegionName    types.String   `tfsdk:"region_name"`
	CSPRegion     types.String   `tfsdk:"csp_region"`
	GroupName     types.String   `tfsdk:"group_name"`
	InstanceType  types.String   `tfsdk:"instance_type"`
	Architecture  types.String   `tfsdk:"architecture"`
	CPU           types.Int64    `tfsdk:"cpu"`
	RAM           types.Int64    `tfsdk:"ram"`
	OSName        types.String   `tfsdk:"os_name"`
	OSVersion     types.String   `tfsdk:"os_version"`
	Image         types.String   `tfsdk:"image"`
	Min           types.Int64    `tfsdk:"min"`
	Max           types.Int64    `tfsdk:"max"`
	Desired       types.Int64    `tfsdk:"desired"`
	Health        types.String   `tfsdk:"health"`
	Zones         []types.String `tfsdk:"zones"`
	NetworkName   types.String   `tfsdk:"network_name"`
	SubnetNames   []types.String `tfsdk:"subnet_names"`
	SecurityGroup types.String   `tfsdk:"security_group"`
	ResourceType  types.String   `tfsdk:"resource_type"`
}

// lbListenerModel maps one abstract load-balancer listener (pd-TF-LB): a port +
// protocol the LB accepts traffic on, with optional layer-7 condition values.
type lbListenerModel struct {
	Port       types.Int64    `tfsdk:"port"`
	Protocol   types.String   `tfsdk:"protocol"`
	Conditions []types.String `tfsdk:"conditions"`
}

// lbHealthCheckModel maps the abstract load-balancer health check.
type lbHealthCheckModel struct {
	Protocol           types.String `tfsdk:"protocol"`
	Port               types.Int64  `tfsdk:"port"`
	Path               types.String `tfsdk:"path"`
	IntervalSeconds    types.Int64  `tfsdk:"interval_seconds"`
	HealthyThreshold   types.Int64  `tfsdk:"healthy_threshold"`
	UnhealthyThreshold types.Int64  `tfsdk:"unhealthy_threshold"`
}

// loadBalancerModel maps the abstract `load_balancer` block of a place: listeners
// + a target (the scale-group / fixed VMs to front) + a health check + optional
// stickiness, placed in the place's network across its subnets/zones (pd-TF-LB).
type loadBalancerModel struct {
	Name        types.String        `tfsdk:"name"`
	Listeners   []lbListenerModel   `tfsdk:"listeners"`
	HealthCheck *lbHealthCheckModel `tfsdk:"health_check"`
	Stickiness  types.Bool          `tfsdk:"stickiness"`
	TargetKind  types.String        `tfsdk:"target_kind"`
	TargetName  types.String        `tfsdk:"target_name"`
}

// lbListenerPlanModel is one resolved listener in the load-balancer plan.
type lbListenerPlanModel struct {
	Port       types.Int64    `tfsdk:"port"`
	Protocol   types.String   `tfsdk:"protocol"`
	Conditions []types.String `tfsdk:"conditions"`
}

// lbHealthCheckPlanModel is the resolved, defaulted health check in the plan.
type lbHealthCheckPlanModel struct {
	Protocol           types.String `tfsdk:"protocol"`
	Port               types.Int64  `tfsdk:"port"`
	Path               types.String `tfsdk:"path"`
	IntervalSeconds    types.Int64  `tfsdk:"interval_seconds"`
	HealthyThreshold   types.Int64  `tfsdk:"healthy_threshold"`
	UnhealthyThreshold types.Int64  `tfsdk:"unhealthy_threshold"`
}

// loadBalancerPlanModel is the computed, catalog-resolved concrete load-balancer
// plan (the abstract→concrete ALB / forwarding-rule+backend / DO-LB translation).
type loadBalancerPlanModel struct {
	Provider      types.String            `tfsdk:"provider"`
	CSP           types.String            `tfsdk:"csp"`
	RegionName    types.String            `tfsdk:"region_name"`
	CSPRegion     types.String            `tfsdk:"csp_region"`
	LBName        types.String            `tfsdk:"lb_name"`
	Listeners     []lbListenerPlanModel   `tfsdk:"listeners"`
	HealthCheck   *lbHealthCheckPlanModel `tfsdk:"health_check"`
	Stickiness    types.Bool              `tfsdk:"stickiness"`
	TargetKind    types.String            `tfsdk:"target_kind"`
	TargetName    types.String            `tfsdk:"target_name"`
	Zones         []types.String          `tfsdk:"zones"`
	NetworkName   types.String            `tfsdk:"network_name"`
	SubnetNames   []types.String          `tfsdk:"subnet_names"`
	SecurityGroup types.String            `tfsdk:"security_group"`
	ResourceType  types.String            `tfsdk:"resource_type"`
}

// topologyModel maps the pyxcloud_topology resource state.
type topologyModel struct {
	ID                 types.String             `tfsdk:"id"`
	Name               types.String             `tfsdk:"name"`
	Provider           types.String             `tfsdk:"provider"`
	Region             types.String             `tfsdk:"region"`
	Components         []componentModel         `tfsdk:"components"`
	Network            *networkModel            `tfsdk:"network"`
	NetworkPlan        *networkPlanModel        `tfsdk:"network_plan"`
	SecurityGroup      *securityGroupModel      `tfsdk:"security_group"`
	SecurityGroupPlan  *securityGroupPlanModel  `tfsdk:"security_group_plan"`
	VirtualMachine     *virtualMachineModel     `tfsdk:"virtual_machine"`
	VirtualMachinePlan *virtualMachinePlanModel `tfsdk:"virtual_machine_plan"`
	ScaleGroup         *scaleGroupModel         `tfsdk:"scale_group"`
	ScaleGroupPlan     *scaleGroupPlanModel     `tfsdk:"scale_group_plan"`
	LoadBalancer       *loadBalancerModel       `tfsdk:"load_balancer"`
	LoadBalancerPlan   *loadBalancerPlanModel   `tfsdk:"load_balancer_plan"`
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
			"virtual_machine": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Abstract virtual-machine for the place (pd-TF-EC2-VM): " +
					"canonical sizing (`architecture`, `cpu`, `ram`, `os`) + `count`, placed " +
					"in the place's `network` and attached to its `security_group`. Resolved " +
					"to `aws_instance` / `google_compute_instance` / `digitalocean_droplet` " +
					"for the topology's provider at plan time — the instance type and image " +
					"come from the `virtual_machine` / OS catalog (never invented).",
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "VM/component name; defaults to the topology name.",
					},
					"architecture": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "CPU architecture: `x86_64` (default) or `arm64`.",
					},
					"cpu": schema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Abstract vCPU count, e.g. `2`. Resolved to a concrete instance type.",
					},
					"ram": schema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Abstract RAM in GiB, e.g. `4`. Resolved to a concrete instance type.",
					},
					"os": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Operating system: `ubuntu` (default) or `debian`.",
					},
					"os_version": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "OS version; defaults to the catalog default " +
							"(ubuntu `24.04` / debian `12`).",
					},
					"count": schema.Int64Attribute{
						Optional:            true,
						MarkdownDescription: "Number of instances to create (defaults to 1).",
					},
				},
			},
			"virtual_machine_plan": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "Computed concrete virtual-machine plan: the " +
					"catalog-resolved translation of the abstract `virtual_machine` for the " +
					"topology's provider (instance type from the `virtual_machine` catalog, " +
					"image from the OS catalog).",
				Attributes: map[string]schema.Attribute{
					"provider":       schema.StringAttribute{Computed: true},
					"csp":            schema.StringAttribute{Computed: true},
					"region_name":    schema.StringAttribute{Computed: true},
					"csp_region":     schema.StringAttribute{Computed: true},
					"vm_name":        schema.StringAttribute{Computed: true},
					"instance_type":  schema.StringAttribute{Computed: true},
					"architecture":   schema.StringAttribute{Computed: true},
					"cpu":            schema.Int64Attribute{Computed: true},
					"ram":            schema.Int64Attribute{Computed: true},
					"os_name":        schema.StringAttribute{Computed: true},
					"os_version":     schema.StringAttribute{Computed: true},
					"image":          schema.StringAttribute{Computed: true},
					"network_name":   schema.StringAttribute{Computed: true},
					"subnet_name":    schema.StringAttribute{Computed: true},
					"security_group": schema.StringAttribute{Computed: true},
					"resource_type":  schema.StringAttribute{Computed: true},
					"instances": schema.ListNestedAttribute{
						Computed: true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"name": schema.StringAttribute{Computed: true},
							},
						},
					},
				},
			},
			"scale_group": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Abstract virtual-machine-scale-group for the place " +
					"(pd-TF-ASG): canonical sizing (`architecture`, `cpu`, `ram`, `os`) plus " +
					"autoscale bounds (`min`, `max`, `desired`) and a `health` kind, placed in " +
					"the place's `network` (spread multi-AZ across its subnets) and attached to " +
					"its `security_group`. Resolved to `aws_launch_template` + " +
					"`aws_autoscaling_group` / `google_compute_instance_template` + " +
					"`google_compute_region_instance_group_manager` + autoscaler for the " +
					"topology's provider at plan time. The instance type reuses the same " +
					"`virtual_machine` SKU resolution as the virtual-machine component. " +
					"DigitalOcean has no native VM autoscaling primitive, so it is a hard " +
					"plan-time error (use `managed-kubernetes` instead).",
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Scale-group/component name; defaults to the topology name.",
					},
					"architecture": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "CPU architecture: `x86_64` (default) or `arm64`.",
					},
					"cpu": schema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Abstract vCPU count per instance. Resolved to a concrete instance type.",
					},
					"ram": schema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Abstract RAM in GiB per instance. Resolved to a concrete instance type.",
					},
					"os": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Operating system: `ubuntu` (default) or `debian`.",
					},
					"os_version": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "OS version; defaults to the catalog default " +
							"(ubuntu `24.04` / debian `12`).",
					},
					"min": schema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Minimum instances (>= 0).",
					},
					"max": schema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Maximum instances (>= `min`, >= 1).",
					},
					"desired": schema.Int64Attribute{
						Optional: true,
						MarkdownDescription: "Desired instances within [`min`, `max`]; " +
							"defaults to `min`.",
					},
					"health": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Health-check kind: `ec2` (instance liveness, " +
							"default) or `elb` (load-balancer health, which also replaces " +
							"unhealthy instances).",
					},
				},
			},
			"scale_group_plan": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "Computed concrete scale-group plan: the catalog-resolved " +
					"translation of the abstract `scale_group` for the topology's provider " +
					"(instance type from the `virtual_machine` catalog, image from the OS " +
					"catalog, multi-AZ zones from the region catalog).",
				Attributes: map[string]schema.Attribute{
					"provider":      schema.StringAttribute{Computed: true},
					"csp":           schema.StringAttribute{Computed: true},
					"region_name":   schema.StringAttribute{Computed: true},
					"csp_region":    schema.StringAttribute{Computed: true},
					"group_name":    schema.StringAttribute{Computed: true},
					"instance_type": schema.StringAttribute{Computed: true},
					"architecture":  schema.StringAttribute{Computed: true},
					"cpu":           schema.Int64Attribute{Computed: true},
					"ram":           schema.Int64Attribute{Computed: true},
					"os_name":       schema.StringAttribute{Computed: true},
					"os_version":    schema.StringAttribute{Computed: true},
					"image":         schema.StringAttribute{Computed: true},
					"min":           schema.Int64Attribute{Computed: true},
					"max":           schema.Int64Attribute{Computed: true},
					"desired":       schema.Int64Attribute{Computed: true},
					"health":        schema.StringAttribute{Computed: true},
					"zones": schema.ListAttribute{
						Computed:    true,
						ElementType: types.StringType,
					},
					"network_name": schema.StringAttribute{Computed: true},
					"subnet_names": schema.ListAttribute{
						Computed:    true,
						ElementType: types.StringType,
					},
					"security_group": schema.StringAttribute{Computed: true},
					"resource_type":  schema.StringAttribute{Computed: true},
				},
			},
			"load_balancer": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Abstract load-balancer for the place (pd-TF-LB): one or " +
					"more `listeners` (port + protocol), a `target` (the `scale_group` fleet or " +
					"a fixed `virtual_machine`), a `health_check`, and optional `stickiness`, " +
					"placed in the place's `network` (spread multi-AZ across its subnets) and " +
					"attached to its `security_group`. Resolved to `aws_lb` + " +
					"`aws_lb_target_group` + `aws_lb_listener` / a regional GCP forwarding rule + " +
					"backend service + health check / `digitalocean_loadbalancer` for the " +
					"topology's provider at plan time. The target group is wired onto the ASG " +
					"(target_group_arns) so the autoscaled fleet registers automatically.",
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Load-balancer/component name; defaults to the topology name.",
					},
					"listeners": schema.ListNestedAttribute{
						Required:            true,
						MarkdownDescription: "Listeners the LB accepts traffic on (at least one).",
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"port": schema.Int64Attribute{
									Required:            true,
									MarkdownDescription: "Listener port, e.g. `80` / `443`.",
								},
								"protocol": schema.StringAttribute{
									Optional:            true,
									MarkdownDescription: "`http` (default), `https`, or `tcp`.",
								},
								"conditions": schema.ListAttribute{
									Optional:    true,
									ElementType: types.StringType,
									MarkdownDescription: "Optional layer-7 match values (host/path). " +
										"For AWS, bounded by the ALB 5-condition-value-per-rule quota " +
										"(a breach is a hard plan-time error).",
								},
							},
						},
					},
					"health_check": schema.SingleNestedAttribute{
						Optional:            true,
						MarkdownDescription: "Health check against the targets; fields default from the first listener.",
						Attributes: map[string]schema.Attribute{
							"protocol":            schema.StringAttribute{Optional: true},
							"port":                schema.Int64Attribute{Optional: true},
							"path":                schema.StringAttribute{Optional: true},
							"interval_seconds":    schema.Int64Attribute{Optional: true},
							"healthy_threshold":   schema.Int64Attribute{Optional: true},
							"unhealthy_threshold": schema.Int64Attribute{Optional: true},
						},
					},
					"stickiness": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "Enable session affinity (`lb_cookie` on AWS, " +
							"generated-cookie on GCP/DO). Defaults to round-robin.",
					},
					"target_kind": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "What the LB fronts: `scale-group` (default, the " +
							"autoscaled fleet) or `vm` (a fixed virtual-machine).",
					},
					"target_name": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Canonical name of the fronted component; defaults to " +
							"the topology's scale-group / virtual-machine name.",
					},
				},
			},
			"load_balancer_plan": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "Computed concrete load-balancer plan: the catalog-resolved " +
					"translation of the abstract `load_balancer` for the topology's provider " +
					"(multi-AZ zones from the region catalog; provider-standard LB shape).",
				Attributes: map[string]schema.Attribute{
					"provider":    schema.StringAttribute{Computed: true},
					"csp":         schema.StringAttribute{Computed: true},
					"region_name": schema.StringAttribute{Computed: true},
					"csp_region":  schema.StringAttribute{Computed: true},
					"lb_name":     schema.StringAttribute{Computed: true},
					"listeners": schema.ListNestedAttribute{
						Computed: true,
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"port":     schema.Int64Attribute{Computed: true},
								"protocol": schema.StringAttribute{Computed: true},
								"conditions": schema.ListAttribute{
									Computed:    true,
									ElementType: types.StringType,
								},
							},
						},
					},
					"health_check": schema.SingleNestedAttribute{
						Computed: true,
						Attributes: map[string]schema.Attribute{
							"protocol":            schema.StringAttribute{Computed: true},
							"port":                schema.Int64Attribute{Computed: true},
							"path":                schema.StringAttribute{Computed: true},
							"interval_seconds":    schema.Int64Attribute{Computed: true},
							"healthy_threshold":   schema.Int64Attribute{Computed: true},
							"unhealthy_threshold": schema.Int64Attribute{Computed: true},
						},
					},
					"stickiness":  schema.BoolAttribute{Computed: true},
					"target_kind": schema.StringAttribute{Computed: true},
					"target_name": schema.StringAttribute{Computed: true},
					"zones": schema.ListAttribute{
						Computed:    true,
						ElementType: types.StringType,
					},
					"network_name": schema.StringAttribute{Computed: true},
					"subnet_names": schema.ListAttribute{
						Computed:    true,
						ElementType: types.StringType,
					},
					"security_group": schema.StringAttribute{Computed: true},
					"resource_type":  schema.StringAttribute{Computed: true},
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
	if plan.VirtualMachine != nil {
		if _, err := r.translateVM(ctx, plan); err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("virtual_machine"),
				"Virtual-machine not resolvable / invalid against the PyxCloud catalog",
				err.Error(),
			)
		}
	}
	if plan.ScaleGroup != nil {
		if _, err := r.translateScaleGroup(ctx, plan); err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("scale_group"),
				"Virtual-machine-scale-group not resolvable / unsupported against the PyxCloud catalog",
				err.Error(),
			)
		}
	}
	if plan.LoadBalancer != nil {
		if _, err := r.translateLoadBalancer(ctx, plan); err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("load_balancer"),
				"Load-balancer not resolvable / invalid against the PyxCloud catalog",
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
	vmPlan, err := r.translateVM(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Virtual-machine translation failed", err.Error())
		return
	}
	asgPlan, err := r.translateScaleGroup(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Virtual-machine-scale-group translation failed", err.Error())
		return
	}
	lbPlan, err := r.translateLoadBalancer(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Load-balancer translation failed", err.Error())
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
	state.VirtualMachine = plan.VirtualMachine
	state.VirtualMachinePlan = vmPlan
	state.ScaleGroup = plan.ScaleGroup
	state.ScaleGroupPlan = asgPlan
	state.LoadBalancer = plan.LoadBalancer
	state.LoadBalancerPlan = lbPlan
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

// translateVM resolves the abstract virtual_machine block into a concrete plan
// via the catalog (instance type from `virtual_machine`, image from the OS
// catalog). Returns (nil, nil) when the topology declares no virtual_machine.
func (r *topologyResource) translateVM(ctx context.Context, m topologyModel) (*virtualMachinePlanModel, error) {
	if m.VirtualMachine == nil {
		return nil, nil
	}
	vm := m.VirtualMachine

	name := vm.Name.ValueString()
	if name == "" {
		name = m.Name.ValueString()
	}
	network, subnet, sg := "", "", ""
	if m.Network != nil {
		network = m.Name.ValueString()
		// Default placement: the first network subnet (production-subnet-1).
		if len(m.Network.Subnets) > 0 {
			subnet = fmt.Sprintf("%s-subnet-1", m.Name.ValueString())
		}
	}
	if m.SecurityGroup != nil {
		sg = m.SecurityGroup.Name.ValueString()
		if sg == "" {
			sg = m.Name.ValueString()
		}
	}

	spec := catalog.VMSpec{
		Name:          name,
		Region:        m.Region.ValueString(),
		Provider:      m.Provider.ValueString(),
		Architecture:  vm.Architecture.ValueString(),
		CPU:           int(vm.CPU.ValueInt64()),
		RAM:           int(vm.RAM.ValueInt64()),
		OS:            vm.OS.ValueString(),
		OSVersion:     vm.OSVersion.ValueString(),
		Count:         int(vm.Count.ValueInt64()),
		Network:       network,
		Subnet:        subnet,
		SecurityGroup: sg,
	}
	cp, err := catalog.TranslateVM(ctx, r.catalog, spec)
	if err != nil {
		return nil, err
	}

	out := &virtualMachinePlanModel{
		Provider:      types.StringValue(cp.Provider),
		CSP:           types.StringValue(cp.CSP),
		RegionName:    types.StringValue(cp.RegionName),
		CSPRegion:     types.StringValue(cp.CSPRegion),
		VMName:        types.StringValue(cp.VMName),
		InstanceType:  types.StringValue(cp.InstanceType),
		Architecture:  types.StringValue(cp.Architecture),
		CPU:           types.Int64Value(int64(cp.CPU)),
		RAM:           types.Int64Value(int64(cp.RAM)),
		OSName:        types.StringValue(cp.OSName),
		OSVersion:     types.StringValue(cp.OSVersion),
		Image:         types.StringValue(cp.Image),
		NetworkName:   types.StringValue(cp.NetworkName),
		SubnetName:    types.StringValue(cp.SubnetName),
		SecurityGroup: types.StringValue(cp.SecurityGroup),
		ResourceType:  types.StringValue(cp.ResourceType),
	}
	for _, inst := range cp.Instances {
		out.Instances = append(out.Instances, vmInstancePlanModel{Name: types.StringValue(inst.Name)})
	}
	return out, nil
}

// translateScaleGroup resolves the abstract scale_group block into a concrete
// plan via the catalog. The instance type reuses the SAME `virtual_machine` SKU
// resolution as the virtual-machine component; the multi-AZ zones come from the
// region catalog. Returns (nil, nil) when no scale_group is declared.
func (r *topologyResource) translateScaleGroup(ctx context.Context, m topologyModel) (*scaleGroupPlanModel, error) {
	if m.ScaleGroup == nil {
		return nil, nil
	}
	sg := m.ScaleGroup

	name := sg.Name.ValueString()
	if name == "" {
		name = m.Name.ValueString()
	}
	network, sgName := "", ""
	var subnets []string
	if m.Network != nil {
		network = m.Name.ValueString()
		// Spread the group across all the network's subnets (multi-AZ).
		for i := range m.Network.Subnets {
			subnets = append(subnets, fmt.Sprintf("%s-subnet-%d", m.Name.ValueString(), i+1))
		}
	}
	if m.SecurityGroup != nil {
		sgName = m.SecurityGroup.Name.ValueString()
		if sgName == "" {
			sgName = m.Name.ValueString()
		}
	}

	spec := catalog.ScaleGroupSpec{
		Name:          name,
		Region:        m.Region.ValueString(),
		Provider:      m.Provider.ValueString(),
		Architecture:  sg.Architecture.ValueString(),
		CPU:           int(sg.CPU.ValueInt64()),
		RAM:           int(sg.RAM.ValueInt64()),
		OS:            sg.OS.ValueString(),
		OSVersion:     sg.OSVersion.ValueString(),
		Min:           int(sg.Min.ValueInt64()),
		Max:           int(sg.Max.ValueInt64()),
		Desired:       int(sg.Desired.ValueInt64()),
		Health:        sg.Health.ValueString(),
		Network:       network,
		Subnets:       subnets,
		SecurityGroup: sgName,
	}
	cp, err := catalog.TranslateScaleGroup(ctx, r.catalog, spec)
	if err != nil {
		return nil, err
	}

	out := &scaleGroupPlanModel{
		Provider:      types.StringValue(cp.Provider),
		CSP:           types.StringValue(cp.CSP),
		RegionName:    types.StringValue(cp.RegionName),
		CSPRegion:     types.StringValue(cp.CSPRegion),
		GroupName:     types.StringValue(cp.GroupName),
		InstanceType:  types.StringValue(cp.InstanceType),
		Architecture:  types.StringValue(cp.Architecture),
		CPU:           types.Int64Value(int64(cp.CPU)),
		RAM:           types.Int64Value(int64(cp.RAM)),
		OSName:        types.StringValue(cp.OSName),
		OSVersion:     types.StringValue(cp.OSVersion),
		Image:         types.StringValue(cp.Image),
		Min:           types.Int64Value(int64(cp.Min)),
		Max:           types.Int64Value(int64(cp.Max)),
		Desired:       types.Int64Value(int64(cp.Desired)),
		Health:        types.StringValue(cp.Health),
		NetworkName:   types.StringValue(cp.NetworkName),
		SecurityGroup: types.StringValue(cp.SecurityGroup),
		ResourceType:  types.StringValue(cp.ResourceType),
	}
	for _, z := range cp.Zones {
		out.Zones = append(out.Zones, types.StringValue(z))
	}
	for _, s := range cp.SubnetNames {
		out.SubnetNames = append(out.SubnetNames, types.StringValue(s))
	}
	return out, nil
}

// translateLoadBalancer resolves the abstract load_balancer block into a concrete
// plan via the catalog. The multi-AZ zones come from the region catalog; the LB
// fronts the sibling scale-group (default) or virtual-machine, spreads across all
// the network's subnets, and attaches the place's security-group. Returns
// (nil, nil) when no load_balancer is declared.
func (r *topologyResource) translateLoadBalancer(ctx context.Context, m topologyModel) (*loadBalancerPlanModel, error) {
	if m.LoadBalancer == nil {
		return nil, nil
	}
	lb := m.LoadBalancer

	name := lb.Name.ValueString()
	if name == "" {
		name = m.Name.ValueString()
	}
	network, sgName := "", ""
	var subnets []string
	if m.Network != nil {
		network = m.Name.ValueString()
		// Spread the LB across all the network's subnets (multi-AZ, internet-facing).
		for i := range m.Network.Subnets {
			subnets = append(subnets, fmt.Sprintf("%s-subnet-%d", m.Name.ValueString(), i+1))
		}
	}
	if m.SecurityGroup != nil {
		sgName = m.SecurityGroup.Name.ValueString()
		if sgName == "" {
			sgName = m.Name.ValueString()
		}
	}

	listeners := make([]catalog.LBListenerSpec, 0, len(lb.Listeners))
	for _, lm := range lb.Listeners {
		conditions := make([]string, 0, len(lm.Conditions))
		for _, c := range lm.Conditions {
			conditions = append(conditions, c.ValueString())
		}
		listeners = append(listeners, catalog.LBListenerSpec{
			Port:       int(lm.Port.ValueInt64()),
			Protocol:   lm.Protocol.ValueString(),
			Conditions: conditions,
		})
	}

	var hc catalog.LBHealthCheckSpec
	if lb.HealthCheck != nil {
		hc = catalog.LBHealthCheckSpec{
			Protocol:           lb.HealthCheck.Protocol.ValueString(),
			Port:               int(lb.HealthCheck.Port.ValueInt64()),
			Path:               lb.HealthCheck.Path.ValueString(),
			IntervalSeconds:    int(lb.HealthCheck.IntervalSeconds.ValueInt64()),
			HealthyThreshold:   int(lb.HealthCheck.HealthyThreshold.ValueInt64()),
			UnhealthyThreshold: int(lb.HealthCheck.UnhealthyThreshold.ValueInt64()),
		}
	}

	// Default the target to the sibling scale-group, else the virtual-machine.
	targetKind := lb.TargetKind.ValueString()
	targetName := lb.TargetName.ValueString()
	if targetName == "" {
		if m.ScaleGroup != nil {
			targetName = m.ScaleGroup.Name.ValueString()
			if targetName == "" {
				targetName = m.Name.ValueString()
			}
			if targetKind == "" {
				targetKind = catalog.LBTargetScaleGroup
			}
		} else if m.VirtualMachine != nil {
			targetName = m.VirtualMachine.Name.ValueString()
			if targetName == "" {
				targetName = m.Name.ValueString()
			}
			if targetKind == "" {
				targetKind = catalog.LBTargetVM
			}
		}
	}

	spec := catalog.LoadBalancerSpec{
		Name:          name,
		Region:        m.Region.ValueString(),
		Provider:      m.Provider.ValueString(),
		Listeners:     listeners,
		HealthCheck:   hc,
		Stickiness:    lb.Stickiness.ValueBool(),
		TargetKind:    targetKind,
		TargetName:    targetName,
		Network:       network,
		Subnets:       subnets,
		SecurityGroup: sgName,
	}
	cp, err := catalog.TranslateLoadBalancer(ctx, r.catalog, spec)
	if err != nil {
		return nil, err
	}

	out := &loadBalancerPlanModel{
		Provider:      types.StringValue(cp.Provider),
		CSP:           types.StringValue(cp.CSP),
		RegionName:    types.StringValue(cp.RegionName),
		CSPRegion:     types.StringValue(cp.CSPRegion),
		LBName:        types.StringValue(cp.LBName),
		Stickiness:    types.BoolValue(cp.Stickiness),
		TargetKind:    types.StringValue(cp.TargetKind),
		TargetName:    types.StringValue(cp.TargetName),
		NetworkName:   types.StringValue(cp.NetworkName),
		SecurityGroup: types.StringValue(cp.SecurityGroup),
		ResourceType:  types.StringValue(cp.ResourceType),
		HealthCheck: &lbHealthCheckPlanModel{
			Protocol:           types.StringValue(cp.HealthCheck.Protocol),
			Port:               types.Int64Value(int64(cp.HealthCheck.Port)),
			Path:               types.StringValue(cp.HealthCheck.Path),
			IntervalSeconds:    types.Int64Value(int64(cp.HealthCheck.IntervalSeconds)),
			HealthyThreshold:   types.Int64Value(int64(cp.HealthCheck.HealthyThreshold)),
			UnhealthyThreshold: types.Int64Value(int64(cp.HealthCheck.UnhealthyThreshold)),
		},
	}
	for _, l := range cp.Listeners {
		conditions := make([]types.String, 0, len(l.Conditions))
		for _, c := range l.Conditions {
			conditions = append(conditions, types.StringValue(c))
		}
		out.Listeners = append(out.Listeners, lbListenerPlanModel{
			Port:       types.Int64Value(int64(l.Port)),
			Protocol:   types.StringValue(l.Protocol),
			Conditions: conditions,
		})
	}
	for _, z := range cp.Zones {
		out.Zones = append(out.Zones, types.StringValue(z))
	}
	for _, s := range cp.SubnetNames {
		out.SubnetNames = append(out.SubnetNames, types.StringValue(s))
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
	refreshed.VirtualMachine = state.VirtualMachine
	if refreshed.VirtualMachine != nil {
		vmPlan, terr := r.translateVM(ctx, refreshed)
		if terr != nil {
			resp.Diagnostics.AddError("Virtual-machine translation failed", terr.Error())
			return
		}
		refreshed.VirtualMachinePlan = vmPlan
	}
	refreshed.ScaleGroup = state.ScaleGroup
	if refreshed.ScaleGroup != nil {
		asgPlan, terr := r.translateScaleGroup(ctx, refreshed)
		if terr != nil {
			resp.Diagnostics.AddError("Virtual-machine-scale-group translation failed", terr.Error())
			return
		}
		refreshed.ScaleGroupPlan = asgPlan
	}
	refreshed.LoadBalancer = state.LoadBalancer
	if refreshed.LoadBalancer != nil {
		lbPlan, terr := r.translateLoadBalancer(ctx, refreshed)
		if terr != nil {
			resp.Diagnostics.AddError("Load-balancer translation failed", terr.Error())
			return
		}
		refreshed.LoadBalancerPlan = lbPlan
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
	vmPlan, err := r.translateVM(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Virtual-machine translation failed", err.Error())
		return
	}
	asgPlan, err := r.translateScaleGroup(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Virtual-machine-scale-group translation failed", err.Error())
		return
	}
	lbPlan, err := r.translateLoadBalancer(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Load-balancer translation failed", err.Error())
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
	newState.VirtualMachine = plan.VirtualMachine
	newState.VirtualMachinePlan = vmPlan
	newState.ScaleGroup = plan.ScaleGroup
	newState.ScaleGroupPlan = asgPlan
	newState.LoadBalancer = plan.LoadBalancer
	newState.LoadBalancerPlan = lbPlan
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
