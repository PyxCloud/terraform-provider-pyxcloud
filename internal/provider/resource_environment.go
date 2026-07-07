package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// environmentResource is Mode A of DEPLOY-GATE.md: `terraform apply` of a
// pyxcloud_environment turns a canonical topology into a REAL environment by
// (1) asking the backend to translate it to concrete provider terraform
// (/api/translate) and (2) running that terraform locally with the ambient
// provider env credentials. No accountBinding, no backend-side creds.
type environmentResource struct {
	client  client.Client
	catalog catalog.Catalog
}

var (
	_ resource.Resource              = (*environmentResource)(nil)
	_ resource.ResourceWithConfigure = (*environmentResource)(nil)
)

// NewEnvironmentResource is the framework resource factory.
func NewEnvironmentResource() resource.Resource {
	return &environmentResource{}
}

// environmentModel maps the pyxcloud_environment resource.
type environmentModel struct {
	ID                              types.String           `tfsdk:"id"`
	Name                            types.String           `tfsdk:"name"`
	Provider                        types.String           `tfsdk:"cloud"`
	Region                          types.String           `tfsdk:"region"`
	CIDR                            types.String           `tfsdk:"cidr"`
	Subnets                         []types.String         `tfsdk:"subnets"`
	Expose                          []types.Int64          `tfsdk:"expose"`
	SecurityRule                    []envSecurityRuleModel `tfsdk:"security_rule"`
	PyxVPC                          []envComponentModel    `tfsdk:"pyx_vpc"`
	PyxNetworkRule                  []envComponentModel    `tfsdk:"pyx_network_rule"`
	PyxAccessPolicy                 []envComponentModel    `tfsdk:"pyx_access_policy"`
	PyxMonitoring                   []envComponentModel    `tfsdk:"pyx_monitoring"`
	PyxDNS                          []envComponentModel    `tfsdk:"pyx_dns"`
	PyxVirtualMachine               []envComponentModel    `tfsdk:"pyx_virtual_machine"`
	PyxAutoscaleVirtualMachineGroup []envComponentModel    `tfsdk:"pyx_autoscale_virtual_machine_group"`
	PyxDatabase                     []envComponentModel    `tfsdk:"pyx_database"`
	PyxLoadBalancer                 []envComponentModel    `tfsdk:"pyx_load_balancer"`
	PyxCache                        []envComponentModel    `tfsdk:"pyx_cache"`
	PyxObjectStorage                []envComponentModel    `tfsdk:"pyx_object_storage"`
	PyxSecret                       []envComponentModel    `tfsdk:"pyx_secret"`
	PyxQueue                        []envComponentModel    `tfsdk:"pyx_queue"`
	PyxStream                       []envComponentModel    `tfsdk:"pyx_stream"`
	PyxServerlessFunction           []envComponentModel    `tfsdk:"pyx_serverless_function"`
	PyxWebService                   []envComponentModel    `tfsdk:"pyx_web_service"`
	PyxKMS                          []envComponentModel    `tfsdk:"pyx_kms"`
	PyxCDN                          []envComponentModel    `tfsdk:"pyx_cdn"`
	PyxWAF                          []envComponentModel    `tfsdk:"pyx_waf"`
	PyxKubernetes                   []envComponentModel    `tfsdk:"pyx_kubernetes"`
	PyxEmail                        []envComponentModel    `tfsdk:"pyx_email"`
	PyxBlockStorage                 []envComponentModel    `tfsdk:"pyx_block_storage"`
	PyxPrefixList                   []envComponentModel    `tfsdk:"pyx_prefix_list"`
	PyxSynthetics                   []envComponentModel    `tfsdk:"pyx_synthetics"`
	PyxALBAttachment                []envComponentModel    `tfsdk:"pyx_alb_attachment"`
	PyxVPNAccess                    []envComponentModel    `tfsdk:"pyx_vpn_access"`
	AccountBinding                  types.String           `tfsdk:"account_binding"`
	DOProject                       types.String           `tfsdk:"do_project"`
	RemoteState                     *envRemoteStateModel   `tfsdk:"remote_state"`
	VaultHA                         *envVaultHAModel       `tfsdk:"vault_ha"`
	WorkDir                         types.String           `tfsdk:"work_dir"`
	Outputs                         types.Map              `tfsdk:"outputs"`
}

// envVaultHAModel configures the 3-node Raft Vault DROPLET cluster
// (catalog.RenderVaultDropletCluster — the SAME renderer the Mode-B DO baseline
// assembler wires in via DOBaselineOptions.VaultHA) as a first-class block on
// pyxcloud_environment (Mode A). DigitalOcean-only. The environment's own VPC
// (name-net) is reused; there is no separate vpc_ref — that's assembled from the
// environment, never declared here. Secrets (KMS creds, transit token) are passed
// through as plain string references at apply time (e.g. via a terraform variable
// or CI secret) — this schema never stores or generates key material itself.
type envVaultHAModel struct {
	Name         types.String `tfsdk:"name"`
	Seal         types.String `tfsdk:"seal"`
	KMSKeyID     types.String `tfsdk:"kms_key_id"`
	KMSRegion    types.String `tfsdk:"kms_region"`
	TransitAddr  types.String `tfsdk:"transit_addr"`
	TransitToken types.String `tfsdk:"transit_token"`
	NodeCount    types.Int64  `tfsdk:"node_count"`
	ReservedIPs  types.Bool   `tfsdk:"reserved_ips"`
	AWSAccessKey types.String `tfsdk:"aws_access_key_id"`
	AWSSecretKey types.String `tfsdk:"aws_secret_access_key"`
}

// envRemoteStateModel configures an S3-compatible remote backend for the
// environment's terraform state (instead of the default local backend in
// work_dir). DigitalOcean Spaces speaks the S3 backend protocol, so this lets a
// production estate keep its state off the apply host and share it across CI /
// operators. Backend credentials come from the ambient env (AWS_ACCESS_KEY_ID /
// AWS_SECRET_ACCESS_KEY set to the Spaces keys) — never rendered.
type envRemoteStateModel struct {
	Bucket   types.String `tfsdk:"bucket"`
	Key      types.String `tfsdk:"key"`
	Region   types.String `tfsdk:"region"`
	Endpoint types.String `tfsdk:"endpoint"`
}

// envSecurityRuleModel is one explicit ingress rule scoped to an external
// security-group id (e.g. a shared ALB SG from remote-state), instead of the
// 0.0.0.0/0 `expose` shorthand. AWS-only.
type envSecurityRuleModel struct {
	FromPort              types.Int64  `tfsdk:"from_port"`
	ToPort                types.Int64  `tfsdk:"to_port"`
	Protocol              types.String `tfsdk:"protocol"`
	SourceSecurityGroupID types.String `tfsdk:"source_security_group_id"`
}

// envComponentModel is the env-resource-specific component (decoupled from the
// shared topology componentModel so env-only blocks like iam don't churn the
// topology/compare schemas). VM sizing reuses the shared vmTypeModel.
type envComponentModel struct {
	Path                     types.String          `tfsdk:"path"`
	Name                     types.String          `tfsdk:"name"`
	Count                    types.Int64           `tfsdk:"count"`
	Architecture             types.String          `tfsdk:"architecture"`
	CPU                      types.String          `tfsdk:"cpu"`
	RAM                      types.String          `tfsdk:"ram"`
	OSName                   types.String          `tfsdk:"os_name"`
	Min                      types.Int64           `tfsdk:"min"`
	Max                      types.Int64           `tfsdk:"max"`
	Desired                  types.Int64           `tfsdk:"desired"`
	Health                   types.String          `tfsdk:"health"`
	UserData                 types.String          `tfsdk:"user_data"`
	InstanceProfileName      types.String          `tfsdk:"instance_profile"`
	RootDiskGB               types.Int64           `tfsdk:"root_disk_gb"`
	Tag                      types.String          `tfsdk:"tag"`
	SSHKeys                  []types.String        `tfsdk:"ssh_keys"`
	ALBListenerARN           types.String          `tfsdk:"alb_listener_arn"`
	HostHeader               types.String          `tfsdk:"host_header"`
	Port                     types.Int64           `tfsdk:"port"`
	Protocol                 types.String          `tfsdk:"protocol"`
	HealthCheckPath          types.String          `tfsdk:"health_check_path"`
	HealthCheckPortString    types.String          `tfsdk:"health_check_port"`
	ScaleGroupName           types.String          `tfsdk:"scale_group"`
	Priority                 types.Int64           `tfsdk:"priority"`
	AssumeService            types.String          `tfsdk:"assume_service"`
	InlinePolicies           []envIAMPolicyModel   `tfsdk:"inline_policies"`
	ManagedPolicyARNs        []types.String        `tfsdk:"managed_policy_arns"`
	LogGroups                []envLogGroupModel    `tfsdk:"log_groups"`
	Alarms                   []envAlarmModel       `tfsdk:"alarms"`
	ZoneID                   types.String          `tfsdk:"zone_id"`
	Records                  []envDNSRecordModel   `tfsdk:"records"`
	Versioning               types.Bool            `tfsdk:"versioning"`
	Public                   types.Bool            `tfsdk:"public"`
	Description              types.String          `tfsdk:"description"`
	RotationDays             types.Int64           `tfsdk:"rotation_days"`
	Engine                   types.String          `tfsdk:"engine"`
	Version                  types.String          `tfsdk:"version"`
	StorageGB                types.Int64           `tfsdk:"storage_gb"`
	HA                       types.Bool            `tfsdk:"ha"`
	Encrypted                types.Bool            `tfsdk:"encrypted"`
	FIFO                     types.Bool            `tfsdk:"fifo"`
	VisibilityTimeoutSeconds types.Int64           `tfsdk:"visibility_timeout_seconds"`
	MaxReceiveCount          types.Int64           `tfsdk:"max_receive_count"`
	Shards                   types.Int64           `tfsdk:"shards"`
	RetentionHours           types.Int64           `tfsdk:"retention_hours"`
	Runtime                  types.String          `tfsdk:"runtime"`
	RuntimeVersion           types.String          `tfsdk:"runtime_version"`
	Handler                  types.String          `tfsdk:"handler"`
	MemoryMB                 types.Int64           `tfsdk:"memory_mb"`
	TimeoutSeconds           types.Int64           `tfsdk:"timeout_seconds"`
	SourceArtifact           types.String          `tfsdk:"source_artifact"`
	DeletionWindowDays       types.Int64           `tfsdk:"deletion_window_days"`
	MemoryGB                 types.Int64           `tfsdk:"memory_gb"`
	OriginKind               types.String          `tfsdk:"origin_kind"`
	OriginName               types.String          `tfsdk:"origin_name"`
	Scope                    types.String          `tfsdk:"scope"`
	AssociateName            types.String          `tfsdk:"associate_name"`
	NodeCPU                  types.Int64           `tfsdk:"node_cpu"`
	NodeRAM                  types.Int64           `tfsdk:"node_ram"`
	MinNodes                 types.Int64           `tfsdk:"min_nodes"`
	MaxNodes                 types.Int64           `tfsdk:"max_nodes"`
	DesiredNodes             types.Int64           `tfsdk:"desired_nodes"`
	Listeners                []envLBListenerModel  `tfsdk:"listeners"`
	HealthProtocol           types.String          `tfsdk:"health_protocol"`
	Stickiness               types.Bool            `tfsdk:"stickiness"`
	TargetKind               types.String          `tfsdk:"target_kind"`
	TargetName               types.String          `tfsdk:"target_name"`
	TargetTag                types.String          `tfsdk:"target_tag"`
	StableIP                 types.Bool            `tfsdk:"stable_ip"`
	Domain                   types.String          `tfsdk:"domain"`
	SizeGB                   types.Int64           `tfsdk:"size_gb"`
	VolumeType               types.String          `tfsdk:"volume_type"`
	DeviceName               types.String          `tfsdk:"device_name"`
	TargetVM                 types.String          `tfsdk:"target_vm"`
	Entries                  []envPrefixEntryModel `tfsdk:"entries"`
	TargetURL                types.String          `tfsdk:"target_url"`
	ScheduleExpr             types.String          `tfsdk:"schedule_expr"`
	ArtifactBucket           types.String          `tfsdk:"artifact_bucket"`
	ExecRoleARN              types.String          `tfsdk:"exec_role_arn"`
	VPC                      types.String          `tfsdk:"vpc"`
	KeycloakRole             types.String          `tfsdk:"keycloak_role"`
	WireGuardPort            types.Int64           `tfsdk:"wireguard_port"`
	BreakGlassCIDRs          []types.String        `tfsdk:"break_glass_cidrs"`
	AllowlistTable           types.String          `tfsdk:"allowlist_table"`
	PITR                     types.Bool            `tfsdk:"point_in_time_recovery"`
	SourceKind               types.String          `tfsdk:"source_kind"`
	SourceDir                types.String          `tfsdk:"source_dir"`
	ImageRegistryType        types.String          `tfsdk:"image_registry_type"`
	ImageRepository          types.String          `tfsdk:"image_repository"`
	ImageTag                 types.String          `tfsdk:"image_tag"`
	HTTPPort                 types.Int64           `tfsdk:"http_port"`
	InstanceSize             types.String          `tfsdk:"instance_size"`
	InstanceCount            types.Int64           `tfsdk:"instance_count"`
	Env                      types.Map             `tfsdk:"env"`
}

type envScaleGroupModel struct {
	Architecture    types.String `tfsdk:"architecture"`
	CPU             types.String `tfsdk:"cpu"`
	RAM             types.String `tfsdk:"ram"`
	OSName          types.String `tfsdk:"os_name"`
	Min             types.Int64  `tfsdk:"min"`
	Max             types.Int64  `tfsdk:"max"`
	Desired         types.Int64  `tfsdk:"desired"`
	Health          types.String `tfsdk:"health"`
	UserData        types.String `tfsdk:"user_data"`
	InstanceProfile types.String `tfsdk:"instance_profile"`
	RootDiskGB      types.Int64  `tfsdk:"root_disk_gb"`
}

type envAttachToExistingALBModel struct {
	ALBListenerARN  types.String `tfsdk:"alb_listener_arn"`
	HostHeader      types.String `tfsdk:"host_header"`
	Port            types.Int64  `tfsdk:"port"`
	Protocol        types.String `tfsdk:"protocol"`
	HealthCheckPath types.String `tfsdk:"health_check_path"`
	HealthCheckPort types.String `tfsdk:"health_check_port"`
	ScaleGroup      types.String `tfsdk:"scale_group"`
	Priority        types.Int64  `tfsdk:"priority"`
}

type envSyntheticsModel struct {
	TargetURL      types.String `tfsdk:"target_url"`
	Runtime        types.String `tfsdk:"runtime"`
	Handler        types.String `tfsdk:"handler"`
	ScheduleExpr   types.String `tfsdk:"schedule_expr"`
	ArtifactBucket types.String `tfsdk:"artifact_bucket"`
	ExecRoleARN    types.String `tfsdk:"exec_role_arn"`
}

type envBlockStorageModel struct {
	SizeGB     types.Int64  `tfsdk:"size_gb"`
	VolumeType types.String `tfsdk:"volume_type"`
	DeviceName types.String `tfsdk:"device_name"`
	TargetVM   types.String `tfsdk:"target_vm"`
}

type envPrefixEntryModel struct {
	CIDR        types.String `tfsdk:"cidr"`
	Description types.String `tfsdk:"description"`
}

type envPrefixListModel struct {
	Entries []envPrefixEntryModel `tfsdk:"entries"`
}

type envEmailModel struct {
	Domain types.String `tfsdk:"domain"`
}

type envLBListenerModel struct {
	Port     types.Int64  `tfsdk:"port"`
	Protocol types.String `tfsdk:"protocol"`
}

type envLBModel struct {
	Listeners       []envLBListenerModel `tfsdk:"listeners"`
	HealthCheckPath types.String         `tfsdk:"health_check_path"`
	HealthCheckPort types.Int64          `tfsdk:"health_check_port"`
	HealthProtocol  types.String         `tfsdk:"health_protocol"`
	Stickiness      types.Bool           `tfsdk:"stickiness"`
	TargetKind      types.String         `tfsdk:"target_kind"`
	TargetName      types.String         `tfsdk:"target_name"`
}

type envCacheModel struct {
	Engine   types.String `tfsdk:"engine"`
	Version  types.String `tfsdk:"version"`
	MemoryGB types.Int64  `tfsdk:"memory_gb"`
	HA       types.Bool   `tfsdk:"ha"`
}

type envCDNModel struct {
	OriginKind types.String `tfsdk:"origin_kind"`
	OriginName types.String `tfsdk:"origin_name"`
}

type envWAFModel struct {
	Scope         types.String `tfsdk:"scope"`
	AssociateName types.String `tfsdk:"associate_name"`
}

type envK8sModel struct {
	Version      types.String `tfsdk:"version"`
	Architecture types.String `tfsdk:"architecture"`
	NodeCPU      types.Int64  `tfsdk:"node_cpu"`
	NodeRAM      types.Int64  `tfsdk:"node_ram"`
	MinNodes     types.Int64  `tfsdk:"min_nodes"`
	MaxNodes     types.Int64  `tfsdk:"max_nodes"`
	DesiredNodes types.Int64  `tfsdk:"desired_nodes"`
}

type envKMSModel struct {
	Description        types.String `tfsdk:"description"`
	RotationDays       types.Int64  `tfsdk:"rotation_days"`
	DeletionWindowDays types.Int64  `tfsdk:"deletion_window_days"`
}

type envQueueModel struct {
	FIFO                     types.Bool  `tfsdk:"fifo"`
	VisibilityTimeoutSeconds types.Int64 `tfsdk:"visibility_timeout_seconds"`
	MaxReceiveCount          types.Int64 `tfsdk:"max_receive_count"`
}

type envStreamModel struct {
	Shards         types.Int64 `tfsdk:"shards"`
	RetentionHours types.Int64 `tfsdk:"retention_hours"`
}

type envServerlessModel struct {
	Runtime        types.String `tfsdk:"runtime"`
	RuntimeVersion types.String `tfsdk:"runtime_version"`
	Handler        types.String `tfsdk:"handler"`
	MemoryMB       types.Int64  `tfsdk:"memory_mb"`
	TimeoutSeconds types.Int64  `tfsdk:"timeout_seconds"`
	SourceArtifact types.String `tfsdk:"source_artifact"`
}

type envMDBModel struct {
	Engine    types.String `tfsdk:"engine"`
	Version   types.String `tfsdk:"version"`
	CPU       types.Int64  `tfsdk:"cpu"`
	RAM       types.Int64  `tfsdk:"ram"`
	StorageGB types.Int64  `tfsdk:"storage_gb"`
	HA        types.Bool   `tfsdk:"ha"`
	Encrypted types.Bool   `tfsdk:"encrypted"`
}

type envObjectStorageModel struct {
	Versioning types.Bool `tfsdk:"versioning"`
	Public     types.Bool `tfsdk:"public"`
}

type envSecretsModel struct {
	Description  types.String `tfsdk:"description"`
	RotationDays types.Int64  `tfsdk:"rotation_days"`
}

type envDNSRecordModel struct {
	Name    types.String `tfsdk:"name"`
	Type    types.String `tfsdk:"type"`
	Content types.String `tfsdk:"content"`
	TTL     types.Int64  `tfsdk:"ttl"`
	Proxied types.Bool   `tfsdk:"proxied"`
}

type envDNSModel struct {
	ZoneID  types.String        `tfsdk:"zone_id"`
	Records []envDNSRecordModel `tfsdk:"records"`
}

type envLogGroupModel struct {
	Name          types.String `tfsdk:"name"`
	RetentionDays types.Int64  `tfsdk:"retention_days"`
}

type envAlarmModel struct {
	Name               types.String  `tfsdk:"name"`
	Namespace          types.String  `tfsdk:"namespace"`
	MetricName         types.String  `tfsdk:"metric_name"`
	ComparisonOperator types.String  `tfsdk:"comparison_operator"`
	Threshold          types.Float64 `tfsdk:"threshold"`
	EvaluationPeriods  types.Int64   `tfsdk:"evaluation_periods"`
	PeriodSeconds      types.Int64   `tfsdk:"period_seconds"`
	Statistic          types.String  `tfsdk:"statistic"`
}

type envMonitoringModel struct {
	LogGroups []envLogGroupModel `tfsdk:"log_groups"`
	Alarms    []envAlarmModel    `tfsdk:"alarms"`
}

type envIAMPolicyModel struct {
	Name     types.String `tfsdk:"name"`
	Document types.String `tfsdk:"document"`
}

type envIAMModel struct {
	AssumeService     types.String        `tfsdk:"assume_service"`
	InlinePolicies    []envIAMPolicyModel `tfsdk:"inline_policies"`
	ManagedPolicyARNs []types.String      `tfsdk:"managed_policy_arns"`
	InstanceProfile   types.Bool          `tfsdk:"instance_profile"`
}

// modeB reports whether the resource selects the managed-account path (Mode B):
// account_binding is set, so PyxCloud drives the deploy server-side with the
// stored binding's credentials. Empty → Mode A (local env-credential apply).
func (m environmentModel) modeB() bool {
	return !m.AccountBinding.IsNull() && !m.AccountBinding.IsUnknown() && m.AccountBinding.ValueString() != ""
}

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Provisions a real environment from a canonical topology by translating it " +
			"to concrete provider terraform (backend `/api/translate`) and applying it locally with your " +
			"ambient provider env credentials (AWS_*, GOOGLE_*, DIGITALOCEAN_TOKEN). The terraform-native " +
			"replacement for hand-written per-provider terraform — no accountBinding, no backend-held creds.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Environment id (the name).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Environment name, unique per work_dir.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			// NOTE: named `cloud`, not `provider` — `provider` is a reserved Terraform
			// resource meta-argument (provider-config selection), so a schema attribute
			// named `provider` is unsettable from HCL (terraform parses `provider = "aws"`
			// as the meta-arg and then demands a hashicorp/aws provider).
			"cloud": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Cloud provider to deploy to: `aws` | `gcp` | `digitalocean`.",
			},
			"region": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Abstract pyx region_name (e.g. `Dublin`); the backend resolves it to a concrete cspRegion.",
			},
			"cidr": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "VPC/network CIDR for the synthesised environment network. Empty -> the default `10.0.0.0/16`. Set a distinct range (e.g. `10.0.2.0/24`) to run PARALLEL environments in the same cloud account without overlap.",
			},
			"subnets": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Subnet CIDRs the environment spreads across (must fall within `cidr`). Empty -> derived defaults. On DigitalOcean (single VPC per region) set this to the same single range as `cidr`.",
			},
			"expose": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.Int64Type,
				MarkdownDescription: "TCP ports to expose on the environment security group when VM or autoscale VM group components are present. Opens ingress from 0.0.0.0/0 — for an internal port reachable only via a load balancer, use `security_rule` with `source_security_group_id` instead.",
			},
			"security_rule": schema.ListNestedAttribute{
				Optional:            true,
				MarkdownDescription: "Explicit ingress rules scoped to an external security-group id (e.g. a shared ALB SG from remote-state) instead of the 0.0.0.0/0 `expose` shorthand — each rule opens [from_port,to_port]/protocol from `source_security_group_id` only. AWS-only.",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"from_port": schema.Int64Attribute{
						Required:            true,
						MarkdownDescription: "Inclusive low port.",
					},
					"to_port": schema.Int64Attribute{
						Optional:            true,
						MarkdownDescription: "Inclusive high port (defaults to from_port).",
					},
					"protocol": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Protocol: tcp | udp | icmp | all (defaults to tcp).",
					},
					"source_security_group_id": schema.StringAttribute{
						Required:            true,
						MarkdownDescription: "The external security-group id (sg-...) allowed to reach the port.",
					},
				}},
			},
			"account_binding": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Selects the credential source. **Omit** for Mode A (default): the apply runs " +
					"locally with your ambient provider env credentials (AWS_*, GOOGLE_*, DIGITALOCEAN_TOKEN). " +
					"**Set** to a PyxCloud account-binding id for Mode B: a **managed account** where PyxCloud drives " +
					"the deploy server-side with the binding's stored credentials (no creds on the runner). Mode B " +
					"requires the server-side managed-deploy gate (DEPLOY-GATE.md §B) and is enabled once that lands.",
			},
			"do_project": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "DigitalOcean **project name** this environment's resources belong to " +
					"(e.g. `pyxcloud-production` vs `pyxcloud-staging`). Set it per environment so placement is " +
					"decided by config, never the DO account default. When set, scale-group droplet_templates carry " +
					"`project_id`, so **self-healed** droplets stay in the environment's project instead of drifting " +
					"to the default. Omit => account-default (legacy). DigitalOcean-only; ignored on other providers.",
			},
			"remote_state": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Optional S3-compatible remote backend for this environment's terraform state " +
					"(DigitalOcean Spaces speaks the S3 backend protocol). Omit for the default local backend in " +
					"`work_dir`. Backend credentials come from the ambient env (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY " +
					"set to the Spaces keys) — never rendered.",
				Attributes: map[string]schema.Attribute{
					"bucket":   schema.StringAttribute{Optional: true, MarkdownDescription: "State bucket (e.g. `pyxcloud-terraform-state-prod`)."},
					"key":      schema.StringAttribute{Optional: true, MarkdownDescription: "State object key (e.g. `production/do-fra1.tfstate`)."},
					"region":   schema.StringAttribute{Optional: true, MarkdownDescription: "Backend region label (e.g. `fra1` for DO Spaces)."},
					"endpoint": schema.StringAttribute{Optional: true, MarkdownDescription: "S3-compatible endpoint (e.g. `https://fra1.digitaloceanspaces.com`). Empty -> real AWS S3."},
				},
			},
			"vault_ha": schema.SingleNestedAttribute{
				Optional: true,
				MarkdownDescription: "Provisions a 3-node HashiCorp Vault Raft-HA cluster as fixed " +
					"`digitalocean_droplet` resources on this environment's own VPC (the SAME tested renderer " +
					"the DO baseline assembler uses — `catalog.RenderVaultDropletCluster`): a data volume + " +
					"droplet per node, cloud-auto-join peer discovery by DO tag (no baked IPs), a private-only " +
					":8200/:8201 firewall, and a configurable seal stanza. DigitalOcean-only. This is STATEFUL " +
					"infrastructure — review carefully before apply; a raft-snapshot-restored dataset only " +
					"unseals if `seal`/`kms_key_id`/`kms_region` match the source cluster's existing seal key.",
				Attributes: map[string]schema.Attribute{
					"name": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Resource/hostname prefix. Empty -> `pyx-vault`.",
					},
					"seal": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Auto-unseal seal mode: `awskms` (default — the migration bridge: " +
							"reuses the existing AWS KMS seal key so a raft-snapshot-restored dataset unseals) | " +
							"`transit` (end-state: auto-unseal against a separate unseal-Vault's transit engine) | " +
							"`shamir` (no auto-unseal; manual 3-of-5 unseal on every restart).",
					},
					"kms_key_id": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "seal=awskms: the EXISTING AWS KMS key id/ARN the source Vault cluster seals under. A reference, never generated or read here.",
					},
					"kms_region": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "seal=awskms: the AWS region of `kms_key_id` (e.g. `eu-west-1`).",
					},
					"transit_addr": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "seal=transit: address of the unseal-Vault transit endpoint (e.g. `https://unseal-vault.internal:8200`).",
					},
					"transit_token": schema.StringAttribute{
						Optional:            true,
						Sensitive:           true,
						MarkdownDescription: "seal=transit: a Vault token scoped to `transit/` on the unseal-Vault. A reference/injected secret — never generated here.",
					},
					"aws_access_key_id": schema.StringAttribute{
						Optional:  true,
						Sensitive: true,
						MarkdownDescription: "seal=awskms: AWS access key id for the KMS seal (a DO droplet has no " +
							"AWS instance role). A reference to an out-of-band credential (e.g. Vault-sourced or a " +
							"CI secret) — never generated or stored here.",
					},
					"aws_secret_access_key": schema.StringAttribute{
						Optional:            true,
						Sensitive:           true,
						MarkdownDescription: "seal=awskms: AWS secret access key paired with `aws_access_key_id`. Same reference-only discipline.",
					},
					"node_count": schema.Int64Attribute{
						Optional: true,
						MarkdownDescription: "Raft quorum size. Must be exactly `3` (the renderer's fixed, tested " +
							"quorum shape) or omitted; any other value is a plan-time error rather than a silently " +
							"different topology.",
					},
					"reserved_ips": schema.BoolAttribute{
						Optional:            true,
						MarkdownDescription: "Give each node a stable DO reserved IP (survives a droplet roll). Off by default.",
					},
				},
			},
			"pyx_vpc":                             pyxEnvironmentComponentBlock("PyxCloud VPC/network component."),
			"pyx_network_rule":                    pyxEnvironmentComponentBlock("PyxCloud network rule component."),
			"pyx_access_policy":                   pyxEnvironmentComponentBlock("PyxCloud access policy component."),
			"pyx_monitoring":                      pyxEnvironmentComponentBlock("PyxCloud monitoring component."),
			"pyx_dns":                             pyxEnvironmentComponentBlock("PyxCloud DNS component."),
			"pyx_virtual_machine":                 pyxEnvironmentComponentBlock("PyxCloud virtual machine component."),
			"pyx_autoscale_virtual_machine_group": pyxEnvironmentComponentBlock("PyxCloud autoscaling virtual machine group component."),
			"pyx_database":                        pyxEnvironmentComponentBlock("PyxCloud managed database component."),
			"pyx_load_balancer":                   pyxEnvironmentComponentBlock("PyxCloud load balancer component."),
			"pyx_cache":                           pyxEnvironmentComponentBlock("PyxCloud cache component."),
			"pyx_object_storage":                  pyxEnvironmentComponentBlock("PyxCloud object storage component."),
			"pyx_secret":                          pyxEnvironmentComponentBlock("PyxCloud secret manager component."),
			"pyx_queue":                           pyxEnvironmentComponentBlock("PyxCloud queue component."),
			"pyx_stream":                          pyxEnvironmentComponentBlock("PyxCloud stream component."),
			"pyx_serverless_function":             pyxEnvironmentComponentBlock("PyxCloud serverless function component."),
			"pyx_web_service":                     pyxEnvironmentComponentBlock("PyxCloud always-on web service (DO App Platform service)."),
			"pyx_kms":                             pyxEnvironmentComponentBlock("PyxCloud KMS/encryption-key component."),
			"pyx_cdn":                             pyxEnvironmentComponentBlock("PyxCloud CDN component."),
			"pyx_waf":                             pyxEnvironmentComponentBlock("PyxCloud WAF component."),
			"pyx_kubernetes":                      pyxEnvironmentComponentBlock("PyxCloud Kubernetes component."),
			"pyx_email":                           pyxEnvironmentComponentBlock("PyxCloud email component."),
			"pyx_block_storage":                   pyxEnvironmentComponentBlock("PyxCloud block storage component."),
			"pyx_prefix_list":                     pyxEnvironmentComponentBlock("PyxCloud prefix list component."),
			"pyx_synthetics":                      pyxEnvironmentComponentBlock("PyxCloud synthetics component."),
			"pyx_alb_attachment":                  pyxEnvironmentComponentBlock("PyxCloud existing ALB attachment component."),
			"pyx_vpn_access":                      pyxEnvironmentComponentBlock("PyxCloud JIT VPN-access signal: declares that this place needs corporate WireGuard VPN access, and the provider auto-wires the Just-In-Time door (wg-jit security group with SPI-owned ingress + DynamoDB allowlist table + Keycloak-role IAM policy) instead of the manual internal-vpn add-peer.sh / jit-backing terraform. AWS-only. Set keycloak_role to the Keycloak instance's IAM role name."),
			"work_dir": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Directory where the translated terraform runs and its state lives. " +
					"Must be stable for the resource lifecycle; defaults to `${cwd}/.pyxcloud/environments/<name>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"outputs": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Terraform outputs from the applied environment.",
			},
		},
	}
}

func pyxEnvironmentComponentBlock(description string) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional:            true,
		MarkdownDescription: description + " Properties are flat at the `pyx_*` block level.",
		NestedObject:        schema.NestedAttributeObject{Attributes: flatEnvironmentComponentAttributes()},
	}
}

func flatEnvironmentComponentAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"path":         schema.StringAttribute{Optional: true, MarkdownDescription: "Canonical topology path for this component, e.g. `/0/Europe/0/Web-Net/0/app`."},
		"name":         schema.StringAttribute{Required: true, MarkdownDescription: "Component name."},
		"count":        schema.Int64Attribute{Optional: true, Computed: true, MarkdownDescription: "Instance count (defaults to 1)."},
		"architecture": schema.StringAttribute{Optional: true, MarkdownDescription: "CPU architecture, e.g. `x86_64`, `arm64`."},
		"cpu":          schema.StringAttribute{Optional: true, MarkdownDescription: "vCPU count, e.g. `2`."},
		"ram":          schema.StringAttribute{Optional: true, MarkdownDescription: "RAM in GiB, e.g. `4`."},
		"os_name":      schema.StringAttribute{Optional: true, MarkdownDescription: "OS, e.g. `ubuntu`."},
		"min":          schema.Int64Attribute{Optional: true, MarkdownDescription: "Minimum instances."},
		"max":          schema.Int64Attribute{Optional: true, MarkdownDescription: "Maximum instances."},
		"desired":      schema.Int64Attribute{Optional: true, MarkdownDescription: "Desired instances."},
		"health":       schema.StringAttribute{Optional: true, MarkdownDescription: "Health check kind: `ec2` | `elb`."},
		"user_data":    schema.StringAttribute{Optional: true, MarkdownDescription: "cloud-init/bootstrap baked into the launch template."},
		"tag":          schema.StringAttribute{Optional: true, MarkdownDescription: "Extra fleet-selection tag stamped on every instance (a DO load-balancer/firewall targets droplets by tag)."},
		"ssh_keys":     schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Provider SSH-key IDs/fingerprints attached to every instance (DO droplet_template.ssh_keys, required by DO). Empty -> no keys."},
		"instance_profile": schema.StringAttribute{
			Optional:            true,
			MarkdownDescription: "IAM instance-profile name to attach.",
		},
		"root_disk_gb":               schema.Int64Attribute{Optional: true, MarkdownDescription: "Root disk size in GiB (0 = default)."},
		"alb_listener_arn":           schema.StringAttribute{Optional: true, MarkdownDescription: "ARN of an existing ALB listener."},
		"host_header":                schema.StringAttribute{Optional: true, MarkdownDescription: "Host header rule to match."},
		"port":                       schema.Int64Attribute{Optional: true, MarkdownDescription: "Target/listener port."},
		"protocol":                   schema.StringAttribute{Optional: true, MarkdownDescription: "Protocol, e.g. `http`, `https`, or `tcp`."},
		"health_check_path":          schema.StringAttribute{Optional: true},
		"health_check_port":          schema.StringAttribute{Optional: true, MarkdownDescription: "Health check port; defaults to the target port where supported."},
		"scale_group":                schema.StringAttribute{Optional: true, MarkdownDescription: "Name of the sibling scale-group component to attach."},
		"priority":                   schema.Int64Attribute{Optional: true, MarkdownDescription: "Listener rule priority."},
		"assume_service":             schema.StringAttribute{Optional: true, MarkdownDescription: "Principal allowed to assume an IAM role."},
		"managed_policy_arns":        schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Managed policy ARNs to attach."},
		"inline_policies":            inlinePolicyAttribute(),
		"log_groups":                 logGroupsAttribute(),
		"alarms":                     alarmsAttribute(),
		"zone_id":                    schema.StringAttribute{Optional: true, MarkdownDescription: "Cloudflare zone id."},
		"records":                    dnsRecordsAttribute(),
		"versioning":                 schema.BoolAttribute{Optional: true},
		"public":                     schema.BoolAttribute{Optional: true, MarkdownDescription: "PUBLIC read (default false; opt-in only)."},
		"description":                schema.StringAttribute{Optional: true},
		"rotation_days":              schema.Int64Attribute{Optional: true, MarkdownDescription: "0 = no automatic rotation."},
		"engine":                     schema.StringAttribute{Optional: true, MarkdownDescription: "postgres | mysql, or cache engine."},
		"version":                    schema.StringAttribute{Optional: true},
		"storage_gb":                 schema.Int64Attribute{Optional: true},
		"ha":                         schema.BoolAttribute{Optional: true},
		"encrypted":                  schema.BoolAttribute{Optional: true},
		"fifo":                       schema.BoolAttribute{Optional: true},
		"visibility_timeout_seconds": schema.Int64Attribute{Optional: true},
		"max_receive_count":          schema.Int64Attribute{Optional: true},
		"shards":                     schema.Int64Attribute{Optional: true},
		"retention_hours":            schema.Int64Attribute{Optional: true},
		"runtime":                    schema.StringAttribute{Optional: true},
		"runtime_version":            schema.StringAttribute{Optional: true},
		"handler":                    schema.StringAttribute{Optional: true},
		"memory_mb":                  schema.Int64Attribute{Optional: true},
		"timeout_seconds":            schema.Int64Attribute{Optional: true},
		"source_artifact":            schema.StringAttribute{Optional: true},
		"deletion_window_days":       schema.Int64Attribute{Optional: true},
		"memory_gb":                  schema.Int64Attribute{Optional: true},
		"origin_kind":                schema.StringAttribute{Optional: true},
		"origin_name":                schema.StringAttribute{Optional: true},
		"scope":                      schema.StringAttribute{Optional: true},
		"associate_name":             schema.StringAttribute{Optional: true},
		"node_cpu":                   schema.Int64Attribute{Optional: true},
		"node_ram":                   schema.Int64Attribute{Optional: true},
		"min_nodes":                  schema.Int64Attribute{Optional: true},
		"max_nodes":                  schema.Int64Attribute{Optional: true},
		"desired_nodes":              schema.Int64Attribute{Optional: true},
		"listeners":                  lbListenersAttribute(),
		"health_protocol":            schema.StringAttribute{Optional: true},
		"stickiness":                 schema.BoolAttribute{Optional: true},
		"target_kind":                schema.StringAttribute{Optional: true, MarkdownDescription: "vm | scale-group."},
		"target_name":                schema.StringAttribute{Optional: true},
		"target_tag":                 schema.StringAttribute{Optional: true, MarkdownDescription: "Fleet-selection tag the load-balancer fronts (DO droplet_tag, e.g. `pyx-backend`). Empty -> `pyxcloud`."},
		"stable_ip":                  schema.BoolAttribute{Optional: true, MarkdownDescription: "load-balancer: degenerate to a stable public IP (DO `digitalocean_reserved_ip` on the single VM target) instead of a paid balancer. Requires target_kind=vm + target_name; DigitalOcean-only."},
		"domain":                     schema.StringAttribute{Optional: true, MarkdownDescription: "Sending domain to verify."},
		"size_gb":                    schema.Int64Attribute{Optional: true},
		"volume_type":                schema.StringAttribute{Optional: true},
		"device_name":                schema.StringAttribute{Optional: true},
		"target_vm":                  schema.StringAttribute{Optional: true, MarkdownDescription: "VM component to attach to."},
		"entries":                    prefixEntriesAttribute(),
		"target_url":                 schema.StringAttribute{Optional: true},
		"schedule_expr":              schema.StringAttribute{Optional: true},
		"artifact_bucket":            schema.StringAttribute{Optional: true},
		"exec_role_arn":              schema.StringAttribute{Optional: true},
		"vpc":                        schema.StringAttribute{Optional: true, MarkdownDescription: "vpn-access: name of a sibling pyx_vpc the JIT security group attaches to (defaults to the account default VPC)."},
		"keycloak_role":              schema.StringAttribute{Optional: true, MarkdownDescription: "vpn-access: IAM role NAME of the Keycloak instance running the JIT SPI; the generated SG-ingress + DynamoDB policy is attached to it. Required for a vpn-access signal."},
		"wireguard_port":             schema.Int64Attribute{Optional: true, MarkdownDescription: "vpn-access: UDP port the JIT door gates (defaults to 51820)."},
		"break_glass_cidrs":          schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "vpn-access: optional CIDRs allowed to reach the WireGuard port regardless of JIT (admin lockout safety). Empty = pure JIT (dark at rest)."},
		"allowlist_table":            schema.StringAttribute{Optional: true, MarkdownDescription: "vpn-access: DynamoDB allowlist table name the SPI uses (defaults to jit-allowlist)."},
		"point_in_time_recovery":     schema.BoolAttribute{Optional: true, MarkdownDescription: "vpn-access: enable DynamoDB point-in-time recovery on the allowlist table (defaults to true)."},
		"source_kind":                schema.StringAttribute{Optional: true, MarkdownDescription: "web-service: `git` (default) or `image`."},
		"source_dir":                 schema.StringAttribute{Optional: true, MarkdownDescription: "web-service (git source): build source dir (defaults to `/`)."},
		"image_registry_type":        schema.StringAttribute{Optional: true, MarkdownDescription: "web-service (image source): `DOCR` (default) or `DOCKER_HUB`."},
		"image_repository":           schema.StringAttribute{Optional: true, MarkdownDescription: "web-service (image source): container repository."},
		"image_tag":                  schema.StringAttribute{Optional: true, MarkdownDescription: "web-service (image source): image tag."},
		"http_port":                  schema.Int64Attribute{Optional: true, MarkdownDescription: "web-service: container listen port (defaults to 8080)."},
		"instance_size":              schema.StringAttribute{Optional: true, MarkdownDescription: "web-service: App Platform instance size slug (defaults to basic-xxs)."},
		"instance_count":             schema.Int64Attribute{Optional: true, MarkdownDescription: "web-service: always-on replica count (defaults to 1, >=1)."},
		"env":                        schema.MapAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "web-service: plain (non-secret) environment variables."},
	}
}

func inlinePolicyAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional: true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name":     schema.StringAttribute{Required: true},
			"document": schema.StringAttribute{Required: true, MarkdownDescription: "Raw IAM policy JSON."},
		}},
	}
}

func logGroupsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional: true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name":           schema.StringAttribute{Required: true},
			"retention_days": schema.Int64Attribute{Optional: true, MarkdownDescription: "0 = never expire."},
		}},
	}
}

func alarmsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional: true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name":                schema.StringAttribute{Required: true},
			"namespace":           schema.StringAttribute{Required: true},
			"metric_name":         schema.StringAttribute{Required: true},
			"comparison_operator": schema.StringAttribute{Required: true},
			"threshold":           schema.Float64Attribute{Required: true},
			"evaluation_periods":  schema.Int64Attribute{Optional: true},
			"period_seconds":      schema.Int64Attribute{Optional: true},
			"statistic":           schema.StringAttribute{Optional: true},
		}},
	}
}

func dnsRecordsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional: true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"name":    schema.StringAttribute{Required: true},
			"type":    schema.StringAttribute{Required: true, MarkdownDescription: "A | AAAA | CNAME | TXT | MX | ..."},
			"content": schema.StringAttribute{Required: true},
			"ttl":     schema.Int64Attribute{Optional: true, MarkdownDescription: "seconds; 1 = automatic."},
			"proxied": schema.BoolAttribute{Optional: true},
		}},
	}
}

func lbListenersAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional: true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"port":     schema.Int64Attribute{Required: true},
			"protocol": schema.StringAttribute{Required: true, MarkdownDescription: "http | https | tcp."},
		}},
	}
}

func prefixEntriesAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Optional: true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"cidr":        schema.StringAttribute{Required: true},
			"description": schema.StringAttribute{Optional: true},
		}},
	}
}

func (r *environmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("expected *providerData, got %T", req.ProviderData))
		return
	}
	r.client = pd.client
	r.catalog = pd.catalog
}

// assembleInputFromModel builds the catalog-native environment description from
// the resource model, for LOCAL translation (catalog.AssembleHCL).
func (r *environmentResource) assembleInputFromModel(m environmentModel) catalog.AssembleInput {
	in := catalog.AssembleInput{
		Name:      m.Name.ValueString(),
		Provider:  m.Provider.ValueString(),
		Region:    m.Region.ValueString(),
		CIDR:      strings.TrimSpace(m.CIDR.ValueString()),
		DOProject: strings.TrimSpace(m.DOProject.ValueString()),
	}
	if v := m.VaultHA; v != nil {
		seal := strings.TrimSpace(v.Seal.ValueString())
		in.VaultHADroplet = &catalog.AssembleVaultHADroplet{
			Name:           strings.TrimSpace(v.Name.ValueString()),
			Seal:           catalog.VaultSealMode(seal), // "" -> renderer default (awskms)
			KMSKeyID:       v.KMSKeyID.ValueString(),
			KMSRegion:      v.KMSRegion.ValueString(),
			AWSAccessKeyID: v.AWSAccessKey.ValueString(),
			AWSSecretKey:   v.AWSSecretKey.ValueString(),
			TransitAddr:    v.TransitAddr.ValueString(),
			TransitToken:   v.TransitToken.ValueString(),
			ReservedIPs:    v.ReservedIPs.ValueBool(),
			NodeCount:      int(v.NodeCount.ValueInt64()),
		}
	}
	for _, s := range m.Subnets {
		if v := strings.TrimSpace(s.ValueString()); v != "" {
			in.Subnets = append(in.Subnets, v)
		}
	}
	for _, p := range m.Expose {
		in.Expose = append(in.Expose, int(p.ValueInt64()))
	}
	for _, sr := range m.SecurityRule {
		from := int(sr.FromPort.ValueInt64())
		to := int(sr.ToPort.ValueInt64())
		if to == 0 {
			to = from
		}
		proto := sr.Protocol.ValueString()
		if proto == "" {
			proto = catalog.ProtoTCP
		}
		in.IngressRules = append(in.IngressRules, catalog.SecurityRule{
			Direction:          catalog.DirIngress,
			Protocol:           proto,
			FromPort:           from,
			ToPort:             to,
			ExternalSourceSGID: sr.SourceSecurityGroupID.ValueString(),
		})
	}
	for _, typed := range environmentComponentsFromModel(m) {
		cm := typed.model
		count := int(cm.Count.ValueInt64())
		if count <= 0 {
			count = 1
		}
		comp := catalog.AssembleComponent{Path: cm.Path.ValueString(), Name: cm.Name.ValueString(), Type: typed.canonicalType, Count: count}

		if typed.canonicalType == "virtual-machine" || hasFlatVM(cm.Architecture, cm.CPU, cm.RAM, cm.OSName) {
			var vmSSHKeys []string
			for _, k := range cm.SSHKeys {
				vmSSHKeys = append(vmSSHKeys, k.ValueString())
			}
			comp.VM = &catalog.AssembleVM{
				Architecture:    cm.Architecture.ValueString(),
				CPU:             cm.CPU.ValueString(),
				RAM:             cm.RAM.ValueString(),
				OS:              cm.OSName.ValueString(),
				UserData:        cm.UserData.ValueString(),
				InstanceProfile: cm.InstanceProfileName.ValueString(),
				Tag:             cm.Tag.ValueString(),
				SSHKeys:         vmSSHKeys,
			}
		}
		if typed.canonicalType == "virtual-machine-scale-group" || hasScaleGroupFields(cm) {
			var sshKeys []string
			for _, k := range cm.SSHKeys {
				sshKeys = append(sshKeys, k.ValueString())
			}
			comp.ScaleGroup = &catalog.AssembleScaleGroup{
				Architecture:    cm.Architecture.ValueString(),
				CPU:             cm.CPU.ValueString(),
				RAM:             cm.RAM.ValueString(),
				OS:              cm.OSName.ValueString(),
				Min:             int(cm.Min.ValueInt64()),
				Max:             int(cm.Max.ValueInt64()),
				Desired:         int(cm.Desired.ValueInt64()),
				Health:          cm.Health.ValueString(),
				UserData:        cm.UserData.ValueString(),
				InstanceProfile: cm.InstanceProfileName.ValueString(),
				RootDiskGB:      int(cm.RootDiskGB.ValueInt64()),
				Tag:             cm.Tag.ValueString(),
				SSHKeys:         sshKeys,
			}
		}
		if typed.canonicalType == "attach-to-existing-alb" || nonEmptyString(cm.ALBListenerARN) || nonEmptyString(cm.HostHeader) || nonEmptyString(cm.ScaleGroupName) {
			comp.AttachToExistingALB = &catalog.AssembleAttachToExistingALB{
				ALBListenerARN:  cm.ALBListenerARN.ValueString(),
				HostHeader:      cm.HostHeader.ValueString(),
				Port:            int(cm.Port.ValueInt64()),
				Protocol:        cm.Protocol.ValueString(),
				HealthCheckPath: cm.HealthCheckPath.ValueString(),
				HealthCheckPort: cm.HealthCheckPortString.ValueString(),
				ScaleGroup:      cm.ScaleGroupName.ValueString(),
				Priority:        int(cm.Priority.ValueInt64()),
			}
		}
		if typed.canonicalType == "access-policy" || nonEmptyString(cm.AssumeService) || len(cm.ManagedPolicyARNs) > 0 || len(cm.InlinePolicies) > 0 {
			iam := &catalog.AssembleIAM{AssumeService: cm.AssumeService.ValueString()}
			if typed.canonicalType == "access-policy" && strings.EqualFold(strings.TrimSpace(cm.InstanceProfileName.ValueString()), "true") {
				iam.InstanceProfile = true
			}
			for _, arn := range cm.ManagedPolicyARNs {
				iam.ManagedPolicyARNs = append(iam.ManagedPolicyARNs, arn.ValueString())
			}
			for _, p := range cm.InlinePolicies {
				iam.InlinePolicies = append(iam.InlinePolicies, catalog.IAMPolicy{Name: p.Name.ValueString(), Document: p.Document.ValueString()})
			}
			comp.IAM = iam
		}
		if typed.canonicalType == "monitoring" || len(cm.LogGroups) > 0 || len(cm.Alarms) > 0 {
			mon := &catalog.AssembleMonitoring{}
			for _, lg := range cm.LogGroups {
				mon.LogGroups = append(mon.LogGroups, catalog.LogGroup{Name: lg.Name.ValueString(), RetentionDays: int(lg.RetentionDays.ValueInt64())})
			}
			for _, a := range cm.Alarms {
				mon.Alarms = append(mon.Alarms, catalog.MetricAlarm{Name: a.Name.ValueString(), Namespace: a.Namespace.ValueString(), MetricName: a.MetricName.ValueString(), ComparisonOperator: a.ComparisonOperator.ValueString(), Threshold: a.Threshold.ValueFloat64(), EvaluationPeriods: int(a.EvaluationPeriods.ValueInt64()), PeriodSeconds: int(a.PeriodSeconds.ValueInt64()), Statistic: a.Statistic.ValueString()})
			}
			comp.Monitoring = mon
		}
		if typed.canonicalType == "dns" || nonEmptyString(cm.ZoneID) || len(cm.Records) > 0 {
			dns := &catalog.AssembleDNS{ZoneID: cm.ZoneID.ValueString()}
			for _, r := range cm.Records {
				dns.Records = append(dns.Records, catalog.DNSRecord{Name: r.Name.ValueString(), Type: r.Type.ValueString(), Content: r.Content.ValueString(), TTL: int(r.TTL.ValueInt64()), Proxied: r.Proxied.ValueBool()})
			}
			comp.DNS = dns
		}
		if typed.canonicalType == "object-storage" || boolSet(cm.Versioning) || boolSet(cm.Public) {
			comp.ObjectStorage = &catalog.AssembleObjectStorage{Versioning: cm.Versioning.ValueBool(), Public: cm.Public.ValueBool()}
		}
		if typed.canonicalType == "secrets-manager" || nonEmptyString(cm.Description) || intSet(cm.RotationDays) {
			comp.Secrets = &catalog.AssembleSecrets{Description: cm.Description.ValueString(), RotationDays: int(cm.RotationDays.ValueInt64())}
		}
		if typed.canonicalType == "managed-database" || hasDatabaseFields(cm) {
			comp.MDB = &catalog.AssembleMDB{Engine: cm.Engine.ValueString(), Version: cm.Version.ValueString(), CPU: intFromString(cm.CPU), RAM: intFromString(cm.RAM), StorageGB: int(cm.StorageGB.ValueInt64()), HA: cm.HA.ValueBool(), Encrypted: cm.Encrypted.ValueBool()}
		}
		if typed.canonicalType == "managed-queue" || boolSet(cm.FIFO) || intSet(cm.VisibilityTimeoutSeconds) || intSet(cm.MaxReceiveCount) {
			comp.Queue = &catalog.AssembleQueue{FIFO: cm.FIFO.ValueBool(), VisibilityTimeoutSeconds: int(cm.VisibilityTimeoutSeconds.ValueInt64()), MaxReceiveCount: int(cm.MaxReceiveCount.ValueInt64())}
		}
		if typed.canonicalType == "event-streaming" || intSet(cm.Shards) || intSet(cm.RetentionHours) {
			comp.Stream = &catalog.AssembleStream{Shards: int(cm.Shards.ValueInt64()), RetentionHours: int(cm.RetentionHours.ValueInt64())}
		}
		if typed.canonicalType == "serverless-function" || nonEmptyString(cm.Runtime) || nonEmptyString(cm.Handler) || nonEmptyString(cm.SourceArtifact) {
			comp.Serverless = &catalog.AssembleServerless{Runtime: cm.Runtime.ValueString(), RuntimeVersion: cm.RuntimeVersion.ValueString(), Handler: cm.Handler.ValueString(), MemoryMB: int(cm.MemoryMB.ValueInt64()), TimeoutSeconds: int(cm.TimeoutSeconds.ValueInt64()), SourceArtifact: cm.SourceArtifact.ValueString(), Env: envMapFromModel(cm.Env)}
		}
		if typed.canonicalType == "web-service" || nonEmptyString(cm.SourceKind) || nonEmptyString(cm.ImageRepository) || nonEmptyString(cm.InstanceSize) || intSet(cm.HTTPPort) || intSet(cm.InstanceCount) {
			ws := &catalog.AssembleWebService{
				SourceKind:        cm.SourceKind.ValueString(),
				SourceDir:         cm.SourceDir.ValueString(),
				ImageRegistryType: cm.ImageRegistryType.ValueString(),
				ImageRepository:   cm.ImageRepository.ValueString(),
				ImageTag:          cm.ImageTag.ValueString(),
				HTTPPort:          int(cm.HTTPPort.ValueInt64()),
				InstanceSize:      cm.InstanceSize.ValueString(),
				InstanceCount:     int(cm.InstanceCount.ValueInt64()),
				HealthCheckPath:   cm.HealthCheckPath.ValueString(),
				CustomDomain:      cm.Domain.ValueString(),
			}
			ws.Env = envMapFromModel(cm.Env)
			comp.WebService = ws
		}
		if typed.canonicalType == "kms" || intSet(cm.DeletionWindowDays) {
			comp.KMS = &catalog.AssembleKMS{Description: cm.Description.ValueString(), RotationDays: int(cm.RotationDays.ValueInt64()), DeletionWindowDays: int(cm.DeletionWindowDays.ValueInt64())}
		}
		if typed.canonicalType == "cache" || intSet(cm.MemoryGB) {
			comp.Cache = &catalog.AssembleCache{Engine: cm.Engine.ValueString(), Version: cm.Version.ValueString(), MemoryGB: int(cm.MemoryGB.ValueInt64()), HA: cm.HA.ValueBool()}
		}
		if typed.canonicalType == "cdn" || nonEmptyString(cm.OriginKind) || nonEmptyString(cm.OriginName) {
			comp.CDN = &catalog.AssembleCDN{OriginKind: cm.OriginKind.ValueString(), OriginName: cm.OriginName.ValueString()}
		}
		if typed.canonicalType == "waf" || nonEmptyString(cm.Scope) || nonEmptyString(cm.AssociateName) {
			comp.WAF = &catalog.AssembleWAF{Scope: cm.Scope.ValueString(), AssociateName: cm.AssociateName.ValueString()}
		}
		if typed.canonicalType == "kubernetes" || intSet(cm.NodeCPU) || intSet(cm.MinNodes) {
			comp.K8s = &catalog.AssembleK8s{Version: cm.Version.ValueString(), Architecture: cm.Architecture.ValueString(), NodeCPU: int(cm.NodeCPU.ValueInt64()), NodeRAM: int(cm.NodeRAM.ValueInt64()), MinNodes: int(cm.MinNodes.ValueInt64()), MaxNodes: int(cm.MaxNodes.ValueInt64()), DesiredNodes: int(cm.DesiredNodes.ValueInt64())}
		}
		if typed.canonicalType == "load-balancer" || len(cm.Listeners) > 0 || nonEmptyString(cm.TargetKind) || nonEmptyString(cm.TargetName) {
			lb := &catalog.AssembleLB{HealthCheckPath: cm.HealthCheckPath.ValueString(), HealthCheckPort: intFromString(cm.HealthCheckPortString), HealthProtocol: cm.HealthProtocol.ValueString(), Stickiness: cm.Stickiness.ValueBool(), TargetKind: cm.TargetKind.ValueString(), TargetName: cm.TargetName.ValueString(), TargetTag: cm.TargetTag.ValueString(), StableIP: cm.StableIP.ValueBool()}
			for _, l := range cm.Listeners {
				lb.Listeners = append(lb.Listeners, catalog.AssembleLBListener{Port: int(l.Port.ValueInt64()), Protocol: l.Protocol.ValueString()})
			}
			comp.LB = lb
		}
		if typed.canonicalType == "email" || nonEmptyString(cm.Domain) {
			comp.Email = &catalog.AssembleEmail{Domain: cm.Domain.ValueString()}
		}
		if typed.canonicalType == "block-storage" || intSet(cm.SizeGB) || nonEmptyString(cm.TargetVM) {
			comp.BlockStorage = &catalog.AssembleBlockStorage{SizeGB: int(cm.SizeGB.ValueInt64()), VolumeType: cm.VolumeType.ValueString(), DeviceName: cm.DeviceName.ValueString(), TargetVM: cm.TargetVM.ValueString()}
		}
		if typed.canonicalType == "prefix-list" || len(cm.Entries) > 0 {
			pl := &catalog.AssemblePrefixList{}
			for _, e := range cm.Entries {
				pl.Entries = append(pl.Entries, catalog.PrefixEntry{CIDR: e.CIDR.ValueString(), Description: e.Description.ValueString()})
			}
			comp.PrefixList = pl
		}
		if typed.canonicalType == "synthetics" || nonEmptyString(cm.TargetURL) || nonEmptyString(cm.ScheduleExpr) {
			comp.Synthetics = &catalog.AssembleSynthetics{TargetURL: cm.TargetURL.ValueString(), Runtime: cm.Runtime.ValueString(), Handler: cm.Handler.ValueString(), ScheduleExpr: cm.ScheduleExpr.ValueString(), ArtifactBucket: cm.ArtifactBucket.ValueString(), ExecRoleARN: cm.ExecRoleARN.ValueString()}
		}
		if typed.canonicalType == "vpn-access" {
			va := &catalog.AssembleVPNAccess{
				VPC:            cm.VPC.ValueString(),
				KeycloakRole:   cm.KeycloakRole.ValueString(),
				WireGuardPort:  int(cm.WireGuardPort.ValueInt64()),
				AllowlistTable: cm.AllowlistTable.ValueString(),
			}
			for _, c := range cm.BreakGlassCIDRs {
				va.BreakGlassCIDRs = append(va.BreakGlassCIDRs, c.ValueString())
			}
			if !cm.PITR.IsNull() && !cm.PITR.IsUnknown() {
				pitr := cm.PITR.ValueBool()
				va.PITR = &pitr
			}
			comp.VPNAccess = va
		}
		in.Components = append(in.Components, comp)
	}
	return in
}

func hasScaleGroupFields(cm envComponentModel) bool {
	return intSet(cm.Min) || intSet(cm.Max) || intSet(cm.Desired) || nonEmptyString(cm.Health) || nonEmptyString(cm.UserData) || nonEmptyString(cm.InstanceProfileName) || intSet(cm.RootDiskGB)
}

type typedEnvComponentModel struct {
	canonicalType string
	model         envComponentModel
}

func environmentComponentsFromModel(m environmentModel) []typedEnvComponentModel {
	var out []typedEnvComponentModel
	appendComponents := func(canonicalType string, models []envComponentModel) {
		for _, model := range models {
			out = append(out, typedEnvComponentModel{canonicalType: canonicalType, model: model})
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
	appendComponents("vpn-access", m.PyxVPNAccess)
	return out
}

func hasDatabaseFields(cm envComponentModel) bool {
	return nonEmptyString(cm.Engine) || nonEmptyString(cm.Version) || intSet(cm.StorageGB) || boolSet(cm.HA) || boolSet(cm.Encrypted)
}

func normalizeEnvironmentComputedValues(m *environmentModel) {
	normalizeComponentCounts := func(components []envComponentModel) {
		for i := range components {
			if components[i].Count.IsNull() || components[i].Count.IsUnknown() || components[i].Count.ValueInt64() <= 0 {
				components[i].Count = types.Int64Value(1)
			}
		}
	}
	normalizeComponentCounts(m.PyxVPC)
	normalizeComponentCounts(m.PyxNetworkRule)
	normalizeComponentCounts(m.PyxAccessPolicy)
	normalizeComponentCounts(m.PyxMonitoring)
	normalizeComponentCounts(m.PyxDNS)
	normalizeComponentCounts(m.PyxVirtualMachine)
	normalizeComponentCounts(m.PyxAutoscaleVirtualMachineGroup)
	normalizeComponentCounts(m.PyxDatabase)
	normalizeComponentCounts(m.PyxLoadBalancer)
	normalizeComponentCounts(m.PyxCache)
	normalizeComponentCounts(m.PyxObjectStorage)
	normalizeComponentCounts(m.PyxSecret)
	normalizeComponentCounts(m.PyxQueue)
	normalizeComponentCounts(m.PyxStream)
	normalizeComponentCounts(m.PyxServerlessFunction)
	normalizeComponentCounts(m.PyxWebService)
	normalizeComponentCounts(m.PyxKMS)
	normalizeComponentCounts(m.PyxCDN)
	normalizeComponentCounts(m.PyxWAF)
	normalizeComponentCounts(m.PyxKubernetes)
	normalizeComponentCounts(m.PyxEmail)
	normalizeComponentCounts(m.PyxBlockStorage)
	normalizeComponentCounts(m.PyxPrefixList)
	normalizeComponentCounts(m.PyxSynthetics)
	normalizeComponentCounts(m.PyxALBAttachment)
	normalizeComponentCounts(m.PyxVPNAccess)
}

func intSet(v types.Int64) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueInt64() != 0
}

func boolSet(v types.Bool) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueBool()
}

// envMapFromModel converts a tfsdk string map (e.g. a web-service / serverless
// `env` block) into a plain map[string]string, dropping null/unknown. Returns nil
// when unset so downstream renders emit nothing.
func envMapFromModel(m types.Map) map[string]string {
	if m.IsNull() || m.IsUnknown() || len(m.Elements()) == 0 {
		return nil
	}
	out := make(map[string]string, len(m.Elements()))
	for k, v := range m.Elements() {
		if s, ok := v.(types.String); ok {
			out[k] = s.ValueString()
		}
	}
	return out
}

func intFromString(v types.String) int {
	if !nonEmptyString(v) {
		return 0
	}
	n, err := strconv.Atoi(v.ValueString())
	if err != nil {
		return 0
	}
	return n
}

func (r *environmentResource) resolveWorkDir(m *environmentModel) (string, error) {
	if !m.WorkDir.IsNull() && !m.WorkDir.IsUnknown() && m.WorkDir.ValueString() != "" {
		return m.WorkDir.ValueString(), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".pyxcloud", "environments", m.Name.ValueString()), nil
}

// errModeBNotEnabled is returned when the managed-account path is selected before
// the server-side managed-deploy gate is available.
var errModeBNotEnabled = fmt.Errorf("managed-account deploy (Mode B, account_binding set) is not yet enabled: " +
	"it runs server-side with the binding's stored credentials and requires the non-interactive deploy gate " +
	"(DEPLOY-GATE.md §B, pending). For now omit account_binding to use Mode A (apply locally with your ambient " +
	"provider env credentials)")

// renderRemoteBackendHCL renders a `terraform { backend "s3" { ... } }` block for
// the optional remote_state config. Empty when no backend is set (default local
// backend). For an S3-compatible endpoint (DigitalOcean Spaces) it adds the
// skip_*/use_path_style flags terraform's s3 backend needs against non-AWS S3.
func renderRemoteBackendHCL(rs *envRemoteStateModel) string {
	if rs == nil {
		return ""
	}
	bucket := strings.TrimSpace(rs.Bucket.ValueString())
	key := strings.TrimSpace(rs.Key.ValueString())
	if bucket == "" || key == "" {
		return ""
	}
	region := strings.TrimSpace(rs.Region.ValueString())
	if region == "" {
		region = "us-east-1"
	}
	endpoint := strings.TrimSpace(rs.Endpoint.ValueString())
	var b strings.Builder
	b.WriteString("terraform {\n  backend \"s3\" {\n")
	fmt.Fprintf(&b, "    bucket = %q\n", bucket)
	fmt.Fprintf(&b, "    key    = %q\n", key)
	fmt.Fprintf(&b, "    region = %q\n", region)
	if endpoint != "" {
		// S3-compatible (DO Spaces): pin the endpoint and skip the AWS-only checks.
		fmt.Fprintf(&b, "    endpoints                   = { s3 = %q }\n", endpoint)
		b.WriteString("    skip_credentials_validation = true\n")
		b.WriteString("    skip_region_validation      = true\n")
		b.WriteString("    skip_metadata_api_check     = true\n")
		b.WriteString("    skip_requesting_account_id  = true\n")
		b.WriteString("    use_path_style              = true\n")
	}
	b.WriteString("  }\n}\n")
	return b.String()
}

// translateAndApply translates the topology LOCALLY (catalog.AssembleHCL — the
// same Translate/Render the round-trip harness uses, no backend round-trip and no
// token), then runs the resulting terraform in workDir with the ambient provider
// env credentials (Mode A).
func (r *environmentResource) translateAndApply(ctx context.Context, m *environmentModel) (map[string]string, string, error) {
	if r.catalog == nil {
		return nil, "", fmt.Errorf("provider not configured: missing catalog")
	}
	docs, err := catalog.AssembleHCL(ctx, r.catalog, r.assembleInputFromModel(*m))
	if err != nil {
		return nil, "", fmt.Errorf("translate: %w", err)
	}
	// Optional S3-compatible remote backend (e.g. DigitalOcean Spaces) — a separate
	// terraform{} block (valid alongside the assembled required_providers block).
	// Backend creds come from the ambient env (AWS_ACCESS_KEY_ID/SECRET), never rendered.
	if b := renderRemoteBackendHCL(m.RemoteState); b != "" {
		docs = append([]string{b}, docs...)
	}
	if m.modeB() {
		outputs, err := r.client.DeployEnvironment(ctx, m.Name.ValueString(), m.AccountBinding.ValueString(), docs)
		if err != nil {
			return nil, "", err
		}
		return outputs, "", nil
	}
	if len(docs) == 0 {
		return nil, "", fmt.Errorf("translation produced no terraform for this topology")
	}
	workDir, err := r.resolveWorkDir(m)
	if err != nil {
		return nil, "", err
	}
	runner, err := newTFRunner(workDir)
	if err != nil {
		return nil, "", err
	}
	outputs, err := runner.apply(ctx, docs)
	if err != nil {
		return nil, workDir, err
	}
	return outputs, workDir, nil
}

func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	outputs, workDir, err := r.translateAndApply(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Environment apply failed", err.Error())
		return
	}

	plan.ID = types.StringValue(plan.Name.ValueString())
	plan.WorkDir = types.StringValue(workDir)
	normalizeEnvironmentComputedValues(&plan)
	outMap, diags := types.MapValueFrom(ctx, types.StringType, outputs)
	resp.Diagnostics.Append(diags...)
	plan.Outputs = outMap
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.modeB() {
		outputs, err := r.client.RefreshEnvironment(ctx, state.Name.ValueString(), state.AccountBinding.ValueString())
		if err != nil {
			// Refresh is best-effort; don't fail Read on a transient output read error.
			return
		}
		outMap, diags := types.MapValueFrom(ctx, types.StringType, outputs)
		resp.Diagnostics.Append(diags...)
		state.Outputs = outMap
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		return
	}
	workDir, err := r.resolveWorkDir(&state)
	if err != nil {
		resp.Diagnostics.AddError("Resolving work dir failed", err.Error())
		return
	}
	runner, err := newTFRunner(workDir)
	if err != nil {
		// terraform unavailable on this host — keep prior state rather than dropping it.
		return
	}
	outputs, err := runner.refresh(ctx)
	if err != nil {
		// Refresh is best-effort; don't fail Read on a transient output read error.
		return
	}
	outMap, diags := types.MapValueFrom(ctx, types.StringType, outputs)
	resp.Diagnostics.Append(diags...)
	state.Outputs = outMap
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	outputs, workDir, err := r.translateAndApply(ctx, &plan)
	if err != nil {
		resp.Diagnostics.AddError("Environment apply failed", err.Error())
		return
	}
	plan.ID = types.StringValue(plan.Name.ValueString())
	plan.WorkDir = types.StringValue(workDir)
	normalizeEnvironmentComputedValues(&plan)
	outMap, diags := types.MapValueFrom(ctx, types.StringType, outputs)
	resp.Diagnostics.Append(diags...)
	plan.Outputs = outMap
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.modeB() {
		if err := r.client.DestroyEnvironment(ctx, state.Name.ValueString(), state.AccountBinding.ValueString()); err != nil {
			resp.Diagnostics.AddError("Environment destroy failed", err.Error())
			return
		}
		return
	}
	workDir, err := r.resolveWorkDir(&state)
	if err != nil {
		resp.Diagnostics.AddError("Resolving work dir failed", err.Error())
		return
	}
	runner, err := newTFRunner(workDir)
	if err != nil {
		resp.Diagnostics.AddError("terraform unavailable for destroy", err.Error())
		return
	}
	if err := runner.destroy(ctx); err != nil {
		resp.Diagnostics.AddError("Environment destroy failed", err.Error())
		return
	}
}
