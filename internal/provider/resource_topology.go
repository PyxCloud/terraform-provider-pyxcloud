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
	catalog catalog.Catalog
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
	Path         types.String `tfsdk:"path"`
	Name         types.String `tfsdk:"name"`
	Count        types.Int64  `tfsdk:"count"`
	Architecture types.String `tfsdk:"architecture"`
	CPU          types.String `tfsdk:"cpu"`
	RAM          types.String `tfsdk:"ram"`
	OSName       types.String `tfsdk:"os_name"`
	Min          types.Int64  `tfsdk:"min"`
	Max          types.Int64  `tfsdk:"max"`
	Desired      types.Int64  `tfsdk:"desired"`
	Health       types.String `tfsdk:"health"`
}

type pyxComponentType struct {
	BlockName     string
	CanonicalType string
	Description   string
}

var pyxComponentTypes = []pyxComponentType{
	{BlockName: "pyx_vpc", CanonicalType: "vpc", Description: "PyxCloud VPC/network component."},
	{BlockName: "pyx_network_rule", CanonicalType: "network-rule", Description: "PyxCloud network rule component."},
	{BlockName: "pyx_access_policy", CanonicalType: "access-policy", Description: "PyxCloud access policy component."},
	{BlockName: "pyx_monitoring", CanonicalType: "monitoring", Description: "PyxCloud monitoring component."},
	{BlockName: "pyx_dns", CanonicalType: "dns", Description: "PyxCloud DNS component."},
	{BlockName: "pyx_virtual_machine", CanonicalType: "virtual-machine", Description: "PyxCloud virtual machine component."},
	{BlockName: "pyx_autoscale_virtual_machine_group", CanonicalType: "virtual-machine-scale-group", Description: "PyxCloud autoscaling virtual machine group component."},
	{BlockName: "pyx_database", CanonicalType: "managed-database", Description: "PyxCloud managed database component."},
	{BlockName: "pyx_load_balancer", CanonicalType: "load-balancer", Description: "PyxCloud load balancer component."},
	{BlockName: "pyx_cache", CanonicalType: "cache", Description: "PyxCloud cache component."},
	{BlockName: "pyx_object_storage", CanonicalType: "object-storage", Description: "PyxCloud object storage component."},
	{BlockName: "pyx_secret", CanonicalType: "secrets-manager", Description: "PyxCloud secret manager component."},
	{BlockName: "pyx_queue", CanonicalType: "managed-queue", Description: "PyxCloud queue component."},
	{BlockName: "pyx_stream", CanonicalType: "event-streaming", Description: "PyxCloud stream component."},
	{BlockName: "pyx_serverless_function", CanonicalType: "serverless-function", Description: "PyxCloud serverless function component."},
	{BlockName: "pyx_kms", CanonicalType: "kms", Description: "PyxCloud KMS/encryption-key component."},
	{BlockName: "pyx_cdn", CanonicalType: "cdn", Description: "PyxCloud CDN component."},
	{BlockName: "pyx_waf", CanonicalType: "waf", Description: "PyxCloud WAF component."},
	{BlockName: "pyx_kubernetes", CanonicalType: "kubernetes", Description: "PyxCloud Kubernetes component."},
	{BlockName: "pyx_email", CanonicalType: "email", Description: "PyxCloud email component."},
	{BlockName: "pyx_block_storage", CanonicalType: "block-storage", Description: "PyxCloud block storage component."},
	{BlockName: "pyx_prefix_list", CanonicalType: "prefix-list", Description: "PyxCloud prefix list component."},
	{BlockName: "pyx_synthetics", CanonicalType: "synthetics", Description: "PyxCloud synthetics component."},
	{BlockName: "pyx_alb_attachment", CanonicalType: "attach-to-existing-alb", Description: "PyxCloud existing ALB attachment component."},
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

// managedDatabaseModel maps the abstract `managed_database` block of a place
// (pd-TF-MDB): canonical `engine`, `version`, sizing (`cpu`, `ram`), `storage_gb`,
// `ha`, and `encrypted`, placed in the place's `network`/subnets and reachable
// from its `security_group`. The `deletion_protection` / `skip_final_snapshot`
// flags default to the production-safe values (protection on, final snapshot
// taken); the test round-trip override flips them so teardown is clean.
type managedDatabaseModel struct {
	Name               types.String `tfsdk:"name"`
	Engine             types.String `tfsdk:"engine"`
	Version            types.String `tfsdk:"version"`
	CPU                types.Int64  `tfsdk:"cpu"`
	RAM                types.Int64  `tfsdk:"ram"`
	StorageGB          types.Int64  `tfsdk:"storage_gb"`
	HA                 types.Bool   `tfsdk:"ha"`
	Encrypted          types.Bool   `tfsdk:"encrypted"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
	SkipFinalSnapshot  types.Bool   `tfsdk:"skip_final_snapshot"`
}

// managedDatabasePlanModel is the computed, catalog-resolved concrete
// managed-database plan (the abstract→concrete RDS / Cloud SQL / DO-cluster
// translation surfaced back into state).
type managedDatabasePlanModel struct {
	Provider           types.String   `tfsdk:"provider"`
	CSP                types.String   `tfsdk:"csp"`
	RegionName         types.String   `tfsdk:"region_name"`
	CSPRegion          types.String   `tfsdk:"csp_region"`
	DBName             types.String   `tfsdk:"db_name"`
	Engine             types.String   `tfsdk:"engine"`
	EngineVersion      types.String   `tfsdk:"engine_version"`
	DBClass            types.String   `tfsdk:"db_class"`
	Family             types.String   `tfsdk:"family"`
	CPU                types.Int64    `tfsdk:"cpu"`
	RAM                types.Int64    `tfsdk:"ram"`
	StorageGB          types.Int64    `tfsdk:"storage_gb"`
	HA                 types.Bool     `tfsdk:"ha"`
	Encrypted          types.Bool     `tfsdk:"encrypted"`
	DeletionProtection types.Bool     `tfsdk:"deletion_protection"`
	SkipFinalSnapshot  types.Bool     `tfsdk:"skip_final_snapshot"`
	Zones              []types.String `tfsdk:"zones"`
	NetworkName        types.String   `tfsdk:"network_name"`
	SubnetNames        []types.String `tfsdk:"subnet_names"`
	SecurityGroup      types.String   `tfsdk:"security_group"`
	ResourceType       types.String   `tfsdk:"resource_type"`
}

// objectStorageModel maps the abstract `object_storage` block of a place
// (pd-TF-S3): canonical `object-storage { name, versioning, public=false }`,
// placed in the place's region. PRIVATE BY DEFAULT — `public` defaults to false
// and that enforces the provider public-access-block; making a bucket public is
// an explicit opt-in. `force_destroy` defaults to false (production-safe); the
// TEST round-trip override sets it true so a just-created bucket tears down clean.
type objectStorageModel struct {
	Name         types.String `tfsdk:"name"`
	Versioning   types.Bool   `tfsdk:"versioning"`
	Public       types.Bool   `tfsdk:"public"`
	ForceDestroy types.Bool   `tfsdk:"force_destroy"`
}

// objectStoragePlanModel is the computed, catalog-resolved concrete
// object/blob-storage plan (the abstract→concrete S3 / GCS / Spaces translation
// surfaced back into state).
type objectStoragePlanModel struct {
	Provider     types.String `tfsdk:"provider"`
	CSP          types.String `tfsdk:"csp"`
	RegionName   types.String `tfsdk:"region_name"`
	CSPRegion    types.String `tfsdk:"csp_region"`
	BucketName   types.String `tfsdk:"bucket_name"`
	LogicalName  types.String `tfsdk:"logical_name"`
	Versioning   types.Bool   `tfsdk:"versioning"`
	Public       types.Bool   `tfsdk:"public"`
	ForceDestroy types.Bool   `tfsdk:"force_destroy"`
	ResourceType types.String `tfsdk:"resource_type"`
}

// topologyModel maps the pyxcloud_topology resource state.
type topologyModel struct {
	ID                              types.String              `tfsdk:"id"`
	Name                            types.String              `tfsdk:"name"`
	Provider                        types.String              `tfsdk:"cloud"`
	Region                          types.String              `tfsdk:"region"`
	PyxVPC                          []componentModel          `tfsdk:"pyx_vpc"`
	PyxNetworkRule                  []componentModel          `tfsdk:"pyx_network_rule"`
	PyxAccessPolicy                 []componentModel          `tfsdk:"pyx_access_policy"`
	PyxMonitoring                   []componentModel          `tfsdk:"pyx_monitoring"`
	PyxDNS                          []componentModel          `tfsdk:"pyx_dns"`
	PyxVirtualMachine               []componentModel          `tfsdk:"pyx_virtual_machine"`
	PyxAutoscaleVirtualMachineGroup []componentModel          `tfsdk:"pyx_autoscale_virtual_machine_group"`
	PyxDatabase                     []componentModel          `tfsdk:"pyx_database"`
	PyxLoadBalancer                 []componentModel          `tfsdk:"pyx_load_balancer"`
	PyxCache                        []componentModel          `tfsdk:"pyx_cache"`
	PyxObjectStorage                []componentModel          `tfsdk:"pyx_object_storage"`
	PyxSecret                       []componentModel          `tfsdk:"pyx_secret"`
	PyxQueue                        []componentModel          `tfsdk:"pyx_queue"`
	PyxStream                       []componentModel          `tfsdk:"pyx_stream"`
	PyxServerlessFunction           []componentModel          `tfsdk:"pyx_serverless_function"`
	PyxKMS                          []componentModel          `tfsdk:"pyx_kms"`
	PyxCDN                          []componentModel          `tfsdk:"pyx_cdn"`
	PyxWAF                          []componentModel          `tfsdk:"pyx_waf"`
	PyxKubernetes                   []componentModel          `tfsdk:"pyx_kubernetes"`
	PyxEmail                        []componentModel          `tfsdk:"pyx_email"`
	PyxBlockStorage                 []componentModel          `tfsdk:"pyx_block_storage"`
	PyxPrefixList                   []componentModel          `tfsdk:"pyx_prefix_list"`
	PyxSynthetics                   []componentModel          `tfsdk:"pyx_synthetics"`
	PyxALBAttachment                []componentModel          `tfsdk:"pyx_alb_attachment"`
	Network                         *networkModel             `tfsdk:"network"`
	NetworkPlan                     *networkPlanModel         `tfsdk:"network_plan"`
	SecurityGroup                   *securityGroupModel       `tfsdk:"security_group"`
	SecurityGroupPlan               *securityGroupPlanModel   `tfsdk:"security_group_plan"`
	VirtualMachine                  *virtualMachineModel      `tfsdk:"virtual_machine"`
	VirtualMachinePlan              *virtualMachinePlanModel  `tfsdk:"virtual_machine_plan"`
	ScaleGroup                      *scaleGroupModel          `tfsdk:"scale_group"`
	ScaleGroupPlan                  *scaleGroupPlanModel      `tfsdk:"scale_group_plan"`
	LoadBalancer                    *loadBalancerModel        `tfsdk:"load_balancer"`
	LoadBalancerPlan                *loadBalancerPlanModel    `tfsdk:"load_balancer_plan"`
	ManagedDatabase                 *managedDatabaseModel     `tfsdk:"managed_database"`
	ManagedDatabasePlan             *managedDatabasePlanModel `tfsdk:"managed_database_plan"`
	ObjectStorage                   *objectStorageModel       `tfsdk:"object_storage"`
	ObjectStoragePlan               *objectStoragePlanModel   `tfsdk:"object_storage_plan"`
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
			// `cloud`, not `provider`: `provider` is a reserved Terraform root
			// attribute/block name (the provider-config meta-argument), so a schema
			// attribute named `provider` fails validation as a reserved root name.
			"cloud": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Deployment provider: `aws`, `gcp`, or " +
					"`digitalocean` (PyxCloud enabled launch providers).",
			},
			"region": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Abstract PyxCloud macro-region, e.g. `EU West`, " +
					"`US East`, `Asia` — resolved to a concrete CSP region at deploy time.",
			},
			"pyx_vpc":                             pyxTopologyComponentBlock("PyxCloud VPC/network component."),
			"pyx_network_rule":                    pyxTopologyComponentBlock("PyxCloud network rule component."),
			"pyx_access_policy":                   pyxTopologyComponentBlock("PyxCloud access policy component."),
			"pyx_monitoring":                      pyxTopologyComponentBlock("PyxCloud monitoring component."),
			"pyx_dns":                             pyxTopologyComponentBlock("PyxCloud DNS component."),
			"pyx_virtual_machine":                 pyxTopologyComponentBlock("PyxCloud virtual machine component."),
			"pyx_autoscale_virtual_machine_group": pyxTopologyComponentBlock("PyxCloud autoscaling virtual machine group component."),
			"pyx_database":                        pyxTopologyComponentBlock("PyxCloud managed database component."),
			"pyx_load_balancer":                   pyxTopologyComponentBlock("PyxCloud load balancer component."),
			"pyx_cache":                           pyxTopologyComponentBlock("PyxCloud cache component."),
			"pyx_object_storage":                  pyxTopologyComponentBlock("PyxCloud object storage component."),
			"pyx_secret":                          pyxTopologyComponentBlock("PyxCloud secret manager component."),
			"pyx_queue":                           pyxTopologyComponentBlock("PyxCloud queue component."),
			"pyx_stream":                          pyxTopologyComponentBlock("PyxCloud stream component."),
			"pyx_serverless_function":             pyxTopologyComponentBlock("PyxCloud serverless function component."),
			"pyx_kms":                             pyxTopologyComponentBlock("PyxCloud KMS/encryption-key component."),
			"pyx_cdn":                             pyxTopologyComponentBlock("PyxCloud CDN component."),
			"pyx_waf":                             pyxTopologyComponentBlock("PyxCloud WAF component."),
			"pyx_kubernetes":                      pyxTopologyComponentBlock("PyxCloud Kubernetes component."),
			"pyx_email":                           pyxTopologyComponentBlock("PyxCloud email component."),
			"pyx_block_storage":                   pyxTopologyComponentBlock("PyxCloud block storage component."),
			"pyx_prefix_list":                     pyxTopologyComponentBlock("PyxCloud prefix list component."),
			"pyx_synthetics":                      pyxTopologyComponentBlock("PyxCloud synthetics component."),
			"pyx_alb_attachment":                  pyxTopologyComponentBlock("PyxCloud existing ALB attachment component."),
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
			"managed_database": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Abstract managed-database for the place (pd-TF-MDB): " +
					"canonical `engine` (`postgres`/`mysql`), `version`, sizing (`cpu`, `ram`), " +
					"`storage_gb`, `ha`, and `encrypted`, placed in the place's `network`/subnets " +
					"and reachable from its `security_group`. Resolved to `aws_db_instance` (RDS) " +
					"/ `google_sql_database_instance` / `digitalocean_database_cluster` for the " +
					"topology's provider at plan time; the DB instance class comes from the " +
					"`managed_database` catalog (never invented). DATA-SAFETY: changes that would " +
					"force-replace an existing DB (encryption flip, engine change, identifier " +
					"change, storage-type/class-family change) are a hard plan-time ERROR directing " +
					"you to a snapshot-restore migration — never a silent destructive replace " +
					"(the guard added after the 2026-06-15 RDS data-loss incident). Defaults are " +
					"production-safe: `deletion_protection = true` and a final snapshot on destroy.",
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Managed-database/component name; defaults to the topology name.",
					},
					"engine": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Database engine: `postgres` (default) or `mysql`.",
					},
					"version": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Engine version; defaults to the catalog default " +
							"(postgres `16` / mysql `8.0`).",
					},
					"cpu": schema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Abstract vCPU count. Resolved to a concrete DB instance class.",
					},
					"ram": schema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Abstract RAM in GiB. Resolved to a concrete DB instance class.",
					},
					"storage_gb": schema.Int64Attribute{
						Optional: true,
						MarkdownDescription: "Allocated storage in GiB (clamped to a 20 GiB floor; " +
							"defaults to 20).",
					},
					"ha": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "High availability: Multi-AZ (AWS) / REGIONAL (GCP) / " +
							"2-node cluster (DO). Defaults to single-AZ.",
					},
					"encrypted": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "Storage encryption at rest. IMMUTABLE on an existing " +
							"DB — toggling it on a live DB is a hard plan-time error (use a " +
							"copy-snapshot-with-KMS → restore migration).",
					},
					"deletion_protection": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "Guard against accidental destroy. Defaults to `true` " +
							"(production-intent). The TEST round-trip sets this `false` ONLY so " +
							"teardown is clean — that is a test-only override.",
					},
					"skip_final_snapshot": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "Skip the final snapshot on destroy. Defaults to " +
							"`false` (a final snapshot is always taken). The TEST round-trip sets " +
							"this `true` ONLY so teardown is clean — that is a test-only override.",
					},
				},
			},
			"managed_database_plan": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "Computed concrete managed-database plan: the catalog-resolved " +
					"translation of the abstract `managed_database` for the topology's provider " +
					"(DB instance class from the `managed_database` catalog; multi-AZ zones from " +
					"the region catalog; production-safe defaults).",
				Attributes: map[string]schema.Attribute{
					"provider":            schema.StringAttribute{Computed: true},
					"csp":                 schema.StringAttribute{Computed: true},
					"region_name":         schema.StringAttribute{Computed: true},
					"csp_region":          schema.StringAttribute{Computed: true},
					"db_name":             schema.StringAttribute{Computed: true},
					"engine":              schema.StringAttribute{Computed: true},
					"engine_version":      schema.StringAttribute{Computed: true},
					"db_class":            schema.StringAttribute{Computed: true},
					"family":              schema.StringAttribute{Computed: true},
					"cpu":                 schema.Int64Attribute{Computed: true},
					"ram":                 schema.Int64Attribute{Computed: true},
					"storage_gb":          schema.Int64Attribute{Computed: true},
					"ha":                  schema.BoolAttribute{Computed: true},
					"encrypted":           schema.BoolAttribute{Computed: true},
					"deletion_protection": schema.BoolAttribute{Computed: true},
					"skip_final_snapshot": schema.BoolAttribute{Computed: true},
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
			"object_storage": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Abstract object/blob-storage for the place (pd-TF-S3): " +
					"canonical `object-storage { name, versioning, public }`, placed in the " +
					"place's region. Resolved to `aws_s3_bucket` (+ versioning + " +
					"public-access-block) / `google_storage_bucket` / `digitalocean_spaces_bucket` " +
					"for the topology's provider at plan time. The bucket name is derived to be " +
					"globally-unique-safe (sanitised name + a deterministic hash of " +
					"csp/region/name). PRIVATE BY DEFAULT: `public` defaults to `false`, which " +
					"enforces the full public-access-block (AWS) / enforced public-access-" +
					"prevention (GCP) / private ACL (DO) — PyxCloud never emits a world-readable " +
					"bucket by default; making it public is an explicit opt-in.",
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Object-storage/component name; defaults to the topology name.",
					},
					"versioning": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "Keep object versions (S3/GCS/Spaces versioning). " +
							"Defaults to disabled.",
					},
					"public": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "Allow PUBLIC read access. Defaults to `false` " +
							"(private). When false, the provider public-access-block is enforced so " +
							"the bucket can never be made world-readable by an errant ACL/policy. " +
							"Set `true` only when you explicitly intend a public bucket.",
					},
					"force_destroy": schema.BoolAttribute{
						Optional: true,
						MarkdownDescription: "Allow Terraform to delete a NON-empty bucket on " +
							"destroy. Defaults to `false` (refuse to drop a bucket that still holds " +
							"objects). The TEST round-trip sets this `true` ONLY so a just-created " +
							"bucket tears down cleanly — that is a test-only override.",
					},
				},
			},
			"object_storage_plan": schema.SingleNestedAttribute{
				Computed: true,
				MarkdownDescription: "Computed concrete object/blob-storage plan: the " +
					"catalog-resolved translation of the abstract `object_storage` for the " +
					"topology's provider (location from the region catalog; globally-unique-safe " +
					"bucket name; private-by-default access controls).",
				Attributes: map[string]schema.Attribute{
					"provider":      schema.StringAttribute{Computed: true},
					"csp":           schema.StringAttribute{Computed: true},
					"region_name":   schema.StringAttribute{Computed: true},
					"csp_region":    schema.StringAttribute{Computed: true},
					"bucket_name":   schema.StringAttribute{Computed: true},
					"logical_name":  schema.StringAttribute{Computed: true},
					"versioning":    schema.BoolAttribute{Computed: true},
					"public":        schema.BoolAttribute{Computed: true},
					"force_destroy": schema.BoolAttribute{Computed: true},
					"resource_type": schema.StringAttribute{Computed: true},
				},
			},
		},
	}
}

func pyxTopologyComponentBlock(description string) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional:            true,
		MarkdownDescription: description + " Properties are flat at the `pyx_*` block level.",
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"path": schema.StringAttribute{
					Optional:            true,
					MarkdownDescription: "Canonical topology path for this component, e.g. `/0/Europe/0/Web-Net/0/app`.",
				},
				"name": schema.StringAttribute{
					Required:            true,
					MarkdownDescription: "Component name, unique within the topology.",
				},
				"count": schema.Int64Attribute{
					Optional: true,
					Computed: true,
					MarkdownDescription: "Number of instances of this component " +
						"(defaults to 1).",
				},
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
				"min":     schema.Int64Attribute{Optional: true, MarkdownDescription: "Minimum instances for autoscale components."},
				"max":     schema.Int64Attribute{Optional: true, MarkdownDescription: "Maximum instances for autoscale components."},
				"desired": schema.Int64Attribute{Optional: true, MarkdownDescription: "Desired instances for autoscale components."},
				"health":  schema.StringAttribute{Optional: true, MarkdownDescription: "Health check kind for autoscale components: `ec2` | `elb`."},
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
	if plan.ManagedDatabase != nil {
		nextDBPlan, err := r.translateManagedDatabase(ctx, plan)
		if err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("managed_database"),
				"Managed-database not resolvable / invalid against the PyxCloud catalog",
				err.Error(),
			)
		} else if !req.State.Raw.IsNull() {
			// DATA-SAFETY GUARD (SPEC §5.6): on an UPDATE (prior state exists), diff
			// the prior resolved DB plan against the new one and BLOCK at plan time
			// any change that would force-replace the live DB and destroy its data
			// (encryption flip, engine change, identifier change, storage-type/
			// class-family change). This is the guard added after the 2026-06-15 RDS
			// data-loss incident — it never silently proceeds with a destructive
			// replacement.
			var state topologyModel
			resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
			if !resp.Diagnostics.HasError() {
				prior := dbPlanModelToCatalog(state.ManagedDatabasePlan)
				next := dbPlanModelToCatalog(nextDBPlan)
				if derr := catalog.CheckManagedDatabaseDataSafety(prior, next); derr != nil {
					resp.Diagnostics.AddAttributeError(
						path.Root("managed_database"),
						"Managed-database change would force-replace the live database (data-loss guard)",
						derr.Error(),
					)
				}
			}
		}
	}
	if plan.ObjectStorage != nil {
		if _, err := r.translateObjectStorage(ctx, plan); err != nil {
			resp.Diagnostics.AddAttributeError(
				path.Root("object_storage"),
				"Object/blob-storage not resolvable / invalid against the PyxCloud catalog",
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
	dbPlan, err := r.translateManagedDatabase(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Managed-database translation failed", err.Error())
		return
	}
	osPlan, err := r.translateObjectStorage(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Object/blob-storage translation failed", err.Error())
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
	state.ManagedDatabase = plan.ManagedDatabase
	state.ManagedDatabasePlan = dbPlan
	state.ObjectStorage = plan.ObjectStorage
	state.ObjectStoragePlan = osPlan
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

// translateManagedDatabase resolves the abstract managed_database block into a
// concrete plan via the catalog (DB instance class from `managed_database`,
// multi-AZ zones from the region catalog). Production-safe defaults
// (deletion_protection true, final snapshot taken) are applied unless the block
// explicitly overrides them. Returns (nil, nil) when none is declared.
func (r *topologyResource) translateManagedDatabase(ctx context.Context, m topologyModel) (*managedDatabasePlanModel, error) {
	if m.ManagedDatabase == nil {
		return nil, nil
	}
	db := m.ManagedDatabase

	name := db.Name.ValueString()
	if name == "" {
		name = m.Name.ValueString()
	}
	network, sgName := "", ""
	var subnets []string
	if m.Network != nil {
		network = m.Name.ValueString()
		// Spread the DB subnet group across all the network's subnets (multi-AZ).
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

	// The flags are Optional bools: a null value takes the production-safe default
	// (handled inside the catalog via nil pointers); a set value overrides.
	var deletionProtection, skipFinalSnapshot *bool
	if !db.DeletionProtection.IsNull() && !db.DeletionProtection.IsUnknown() {
		v := db.DeletionProtection.ValueBool()
		deletionProtection = &v
	}
	if !db.SkipFinalSnapshot.IsNull() && !db.SkipFinalSnapshot.IsUnknown() {
		v := db.SkipFinalSnapshot.ValueBool()
		skipFinalSnapshot = &v
	}

	spec := catalog.ManagedDatabaseSpec{
		Name:               name,
		Region:             m.Region.ValueString(),
		Provider:           m.Provider.ValueString(),
		Engine:             db.Engine.ValueString(),
		Version:            db.Version.ValueString(),
		CPU:                int(db.CPU.ValueInt64()),
		RAM:                int(db.RAM.ValueInt64()),
		StorageGB:          int(db.StorageGB.ValueInt64()),
		HA:                 db.HA.ValueBool(),
		Encrypted:          db.Encrypted.ValueBool(),
		DeletionProtection: deletionProtection,
		SkipFinalSnapshot:  skipFinalSnapshot,
		Network:            network,
		Subnets:            subnets,
		SecurityGroup:      sgName,
	}
	cp, err := catalog.TranslateManagedDatabase(ctx, r.catalog, spec)
	if err != nil {
		return nil, err
	}

	out := &managedDatabasePlanModel{
		Provider:           types.StringValue(cp.Provider),
		CSP:                types.StringValue(cp.CSP),
		RegionName:         types.StringValue(cp.RegionName),
		CSPRegion:          types.StringValue(cp.CSPRegion),
		DBName:             types.StringValue(cp.DBName),
		Engine:             types.StringValue(cp.Engine),
		EngineVersion:      types.StringValue(cp.EngineVersion),
		DBClass:            types.StringValue(cp.DBClass),
		Family:             types.StringValue(cp.Family),
		CPU:                types.Int64Value(int64(cp.CPU)),
		RAM:                types.Int64Value(int64(cp.RAM)),
		StorageGB:          types.Int64Value(int64(cp.StorageGB)),
		HA:                 types.BoolValue(cp.HA),
		Encrypted:          types.BoolValue(cp.Encrypted),
		DeletionProtection: types.BoolValue(cp.DeletionProtection),
		SkipFinalSnapshot:  types.BoolValue(cp.SkipFinalSnapshot),
		NetworkName:        types.StringValue(cp.NetworkName),
		SecurityGroup:      types.StringValue(cp.SecurityGroup),
		ResourceType:       types.StringValue(cp.ResourceType),
	}
	for _, z := range cp.Zones {
		out.Zones = append(out.Zones, types.StringValue(z))
	}
	for _, s := range cp.SubnetNames {
		out.SubnetNames = append(out.SubnetNames, types.StringValue(s))
	}
	return out, nil
}

// translateObjectStorage resolves the abstract object_storage block into a
// concrete plan via the catalog (location from the region catalog; bucket name
// derived globally-unique-safe). PRIVATE BY DEFAULT — an unset `public` resolves
// to false, which enforces the provider public-access-block. `force_destroy`
// defaults to false unless the block explicitly overrides it (the test override).
// Returns (nil, nil) when no object_storage is declared.
func (r *topologyResource) translateObjectStorage(ctx context.Context, m topologyModel) (*objectStoragePlanModel, error) {
	if m.ObjectStorage == nil {
		return nil, nil
	}
	os := m.ObjectStorage

	name := os.Name.ValueString()
	if name == "" {
		name = m.Name.ValueString()
	}

	// force_destroy is an Optional bool: a null value takes the production-safe
	// default (false, handled inside the catalog via a nil pointer); a set value
	// overrides (the test-only true).
	var forceDestroy *bool
	if !os.ForceDestroy.IsNull() && !os.ForceDestroy.IsUnknown() {
		v := os.ForceDestroy.ValueBool()
		forceDestroy = &v
	}

	spec := catalog.ObjectStorageSpec{
		Name:         name,
		Region:       m.Region.ValueString(),
		Provider:     m.Provider.ValueString(),
		Versioning:   os.Versioning.ValueBool(),
		Public:       os.Public.ValueBool(),
		ForceDestroy: forceDestroy,
	}
	cp, err := catalog.TranslateObjectStorage(ctx, r.catalog, spec)
	if err != nil {
		return nil, err
	}

	return &objectStoragePlanModel{
		Provider:     types.StringValue(cp.Provider),
		CSP:          types.StringValue(cp.CSP),
		RegionName:   types.StringValue(cp.RegionName),
		CSPRegion:    types.StringValue(cp.CSPRegion),
		BucketName:   types.StringValue(cp.BucketName),
		LogicalName:  types.StringValue(cp.LogicalName),
		Versioning:   types.BoolValue(cp.Versioning),
		Public:       types.BoolValue(cp.Public),
		ForceDestroy: types.BoolValue(cp.ForceDestroy),
		ResourceType: types.StringValue(cp.ResourceType),
	}, nil
}

// dbPlanModelToCatalog reconstructs the catalog plan view from a stored plan model
// so the data-safety guard can diff prior state against the new plan. Only the
// replacement-forcing attributes the guard inspects are needed.
func dbPlanModelToCatalog(m *managedDatabasePlanModel) *catalog.ManagedDatabasePlan {
	if m == nil {
		return nil
	}
	return &catalog.ManagedDatabasePlan{
		Provider:  m.Provider.ValueString(),
		DBName:    m.DBName.ValueString(),
		Engine:    m.Engine.ValueString(),
		Family:    m.Family.ValueString(),
		Encrypted: m.Encrypted.ValueBool(),
	}
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
	refreshed.ManagedDatabase = state.ManagedDatabase
	if refreshed.ManagedDatabase != nil {
		dbPlan, terr := r.translateManagedDatabase(ctx, refreshed)
		if terr != nil {
			resp.Diagnostics.AddError("Managed-database translation failed", terr.Error())
			return
		}
		refreshed.ManagedDatabasePlan = dbPlan
	}
	refreshed.ObjectStorage = state.ObjectStorage
	if refreshed.ObjectStorage != nil {
		osPlan, terr := r.translateObjectStorage(ctx, refreshed)
		if terr != nil {
			resp.Diagnostics.AddError("Object/blob-storage translation failed", terr.Error())
			return
		}
		refreshed.ObjectStoragePlan = osPlan
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
	dbPlan, err := r.translateManagedDatabase(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Managed-database translation failed", err.Error())
		return
	}
	osPlan, err := r.translateObjectStorage(ctx, plan)
	if err != nil {
		resp.Diagnostics.AddError("Object/blob-storage translation failed", err.Error())
		return
	}
	// DATA-SAFETY GUARD (defence in depth): re-assert at apply time that the change
	// would not force-replace the live DB (ModifyPlan is the primary gate at plan
	// time; this catches any path that reaches Update directly).
	if plan.ManagedDatabase != nil {
		prior := dbPlanModelToCatalog(state.ManagedDatabasePlan)
		next := dbPlanModelToCatalog(dbPlan)
		if derr := catalog.CheckManagedDatabaseDataSafety(prior, next); derr != nil {
			resp.Diagnostics.AddError(
				"Managed-database change would force-replace the live database (data-loss guard)",
				derr.Error(),
			)
			return
		}
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
	newState.ManagedDatabase = plan.ManagedDatabase
	newState.ManagedDatabasePlan = dbPlan
	newState.ObjectStorage = plan.ObjectStorage
	newState.ObjectStoragePlan = osPlan
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
	comps := topologyComponentsFromModel(m)
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
	m := topologyModel{
		ID:       types.StringValue(t.ID),
		Name:     types.StringValue(t.Name),
		Provider: types.StringValue(t.Provider),
		Region:   types.StringValue(t.Region),
	}
	for _, c := range t.Components {
		cm := componentModel{
			Path:         stringValueOrNull(c.Path),
			Name:         types.StringValue(c.Name),
			Count:        types.Int64Value(int64(c.Count)),
			Architecture: stringValueOrNull(c.Architecture),
			CPU:          stringValueOrNull(c.CPU),
			RAM:          stringValueOrNull(c.RAM),
			OSName:       stringValueOrNull(c.OSName),
			Min:          int64ValueOrNull(c.Min),
			Max:          int64ValueOrNull(c.Max),
			Desired:      int64ValueOrNull(c.Desired),
			Health:       stringValueOrNull(c.Health),
		}
		if c.VM != nil {
			cm.Architecture = stringValueOrNull(c.VM.Architecture)
			cm.CPU = stringValueOrNull(c.VM.CPU)
			cm.RAM = stringValueOrNull(c.VM.RAM)
			cm.OSName = stringValueOrNull(c.VM.OS)
		}
		appendTopologyComponentModel(&m, c.Type, cm)
	}
	return m
}

func topologyComponentsFromModel(m topologyModel) []client.Component {
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

func componentModelToClient(canonicalType string, cm componentModel) client.Component {
	count := int(cm.Count.ValueInt64())
	if count <= 0 {
		count = 1
	}
	comp := client.Component{
		Path:         cm.Path.ValueString(),
		Name:         cm.Name.ValueString(),
		Type:         canonicalType,
		Count:        count,
		Architecture: cm.Architecture.ValueString(),
		CPU:          cm.CPU.ValueString(),
		RAM:          cm.RAM.ValueString(),
		OSName:       cm.OSName.ValueString(),
		Min:          int(cm.Min.ValueInt64()),
		Max:          int(cm.Max.ValueInt64()),
		Desired:      int(cm.Desired.ValueInt64()),
		Health:       cm.Health.ValueString(),
	}
	if hasFlatVM(cm.Architecture, cm.CPU, cm.RAM, cm.OSName) {
		comp.VM = &client.VMType{
			Architecture: cm.Architecture.ValueString(),
			CPU:          cm.CPU.ValueString(),
			RAM:          cm.RAM.ValueString(),
			OS:           cm.OSName.ValueString(),
		}
	}
	return comp
}

func appendTopologyComponentModel(m *topologyModel, canonicalType string, cm componentModel) {
	switch canonicalType {
	case "vpc":
		m.PyxVPC = append(m.PyxVPC, cm)
	case "network-rule":
		m.PyxNetworkRule = append(m.PyxNetworkRule, cm)
	case "access-policy":
		m.PyxAccessPolicy = append(m.PyxAccessPolicy, cm)
	case "monitoring":
		m.PyxMonitoring = append(m.PyxMonitoring, cm)
	case "dns":
		m.PyxDNS = append(m.PyxDNS, cm)
	case "virtual-machine":
		m.PyxVirtualMachine = append(m.PyxVirtualMachine, cm)
	case "virtual-machine-scale-group":
		m.PyxAutoscaleVirtualMachineGroup = append(m.PyxAutoscaleVirtualMachineGroup, cm)
	case "managed-database":
		m.PyxDatabase = append(m.PyxDatabase, cm)
	case "load-balancer":
		m.PyxLoadBalancer = append(m.PyxLoadBalancer, cm)
	case "cache":
		m.PyxCache = append(m.PyxCache, cm)
	case "object-storage", "blob-storage":
		m.PyxObjectStorage = append(m.PyxObjectStorage, cm)
	case "secrets-manager":
		m.PyxSecret = append(m.PyxSecret, cm)
	case "managed-queue", "message-queue":
		m.PyxQueue = append(m.PyxQueue, cm)
	case "event-streaming", "event-bus":
		m.PyxStream = append(m.PyxStream, cm)
	case "serverless-function":
		m.PyxServerlessFunction = append(m.PyxServerlessFunction, cm)
	case "kms", "encryption-key":
		m.PyxKMS = append(m.PyxKMS, cm)
	case "cdn", "cdn-service":
		m.PyxCDN = append(m.PyxCDN, cm)
	case "waf", "waf-service":
		m.PyxWAF = append(m.PyxWAF, cm)
	case "kubernetes", "managed-kubernetes":
		m.PyxKubernetes = append(m.PyxKubernetes, cm)
	case "email", "email-service":
		m.PyxEmail = append(m.PyxEmail, cm)
	case "block-storage":
		m.PyxBlockStorage = append(m.PyxBlockStorage, cm)
	case "prefix-list":
		m.PyxPrefixList = append(m.PyxPrefixList, cm)
	case "synthetics", "uptime-check":
		m.PyxSynthetics = append(m.PyxSynthetics, cm)
	case "attach-to-existing-alb":
		m.PyxALBAttachment = append(m.PyxALBAttachment, cm)
	}
}

func hasFlatVM(architecture, cpu, ram, osName types.String) bool {
	return nonEmptyString(architecture) || nonEmptyString(cpu) || nonEmptyString(ram) || nonEmptyString(osName)
}

func nonEmptyString(v types.String) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueString() != ""
}

func stringValueOrNull(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

func int64ValueOrNull(v int) types.Int64 {
	if v == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(int64(v))
}
