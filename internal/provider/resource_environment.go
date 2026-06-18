package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
	ID             types.String        `tfsdk:"id"`
	Name           types.String        `tfsdk:"name"`
	Provider       types.String        `tfsdk:"cloud"`
	Region         types.String        `tfsdk:"region"`
	Components     []envComponentModel `tfsdk:"components"`
	AccountBinding types.String        `tfsdk:"account_binding"`
	WorkDir        types.String        `tfsdk:"work_dir"`
	Outputs        types.Map           `tfsdk:"outputs"`
}

// envComponentModel is the env-resource-specific component (decoupled from the
// shared topology componentModel so env-only blocks like iam don't churn the
// topology/compare schemas). VM sizing reuses the shared vmTypeModel.
type envComponentModel struct {
	Name          types.String           `tfsdk:"name"`
	Type          types.String           `tfsdk:"type"`
	Count         types.Int64            `tfsdk:"count"`
	VM                  *vmTypeModel                 `tfsdk:"vm"`
	ScaleGroup          *envScaleGroupModel          `tfsdk:"scale_group"`
	AttachToExistingALB *envAttachToExistingALBModel `tfsdk:"attach_to_existing_alb"`
	IAM                 *envIAMModel                 `tfsdk:"iam"`
	Monitoring          *envMonitoringModel          `tfsdk:"monitoring"`
	DNS                 *envDNSModel                 `tfsdk:"dns"`
	ObjectStorage       *envObjectStorageModel       `tfsdk:"object_storage"`
	Secrets             *envSecretsModel             `tfsdk:"secrets"`
	MDB                 *envMDBModel                 `tfsdk:"managed_database"`
	Queue               *envQueueModel               `tfsdk:"queue"`
	Stream              *envStreamModel              `tfsdk:"stream"`
	Serverless          *envServerlessModel          `tfsdk:"serverless"`
	KMS                 *envKMSModel                 `tfsdk:"kms"`
	Cache               *envCacheModel               `tfsdk:"cache"`
	CDN                 *envCDNModel                 `tfsdk:"cdn"`
	WAF                 *envWAFModel                 `tfsdk:"waf"`
	K8s                 *envK8sModel                 `tfsdk:"kubernetes"`
	LB                  *envLBModel                  `tfsdk:"load_balancer"`
	Email               *envEmailModel               `tfsdk:"email"`
	BlockStorage        *envBlockStorageModel        `tfsdk:"block_storage"`
	PrefixList          *envPrefixListModel          `tfsdk:"prefix_list"`
	Synthetics          *envSyntheticsModel          `tfsdk:"synthetics"`
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
			"account_binding": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Selects the credential source. **Omit** for Mode A (default): the apply runs " +
					"locally with your ambient provider env credentials (AWS_*, GOOGLE_*, DIGITALOCEAN_TOKEN). " +
					"**Set** to a PyxCloud account-binding id for Mode B: a **managed account** where PyxCloud drives " +
					"the deploy server-side with the binding's stored credentials (no creds on the runner). Mode B " +
					"requires the server-side managed-deploy gate (DEPLOY-GATE.md §B) and is enabled once that lands.",
			},
			"components": schema.ListNestedAttribute{
				Required:            true,
				MarkdownDescription: "Canonical components that make up the environment.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{Required: true, MarkdownDescription: "Component name."},
						"type": schema.StringAttribute{Required: true, MarkdownDescription: "Canonical component type, e.g. `virtual-machine`, `managed-database`, `load-balancer`, `object-storage`."},
						"count": schema.Int64Attribute{
							Optional:            true,
							Computed:            true,
							MarkdownDescription: "Instance count (defaults to 1).",
						},
						"vm": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Sizing for virtual-machine components.",
							Attributes: map[string]schema.Attribute{
								"architecture": schema.StringAttribute{Optional: true, MarkdownDescription: "CPU architecture, e.g. `x86_64`, `arm64`."},
								"cpu":          schema.StringAttribute{Optional: true, MarkdownDescription: "vCPU count, e.g. `2`."},
								"ram":          schema.StringAttribute{Optional: true, MarkdownDescription: "RAM in GiB, e.g. `4`."},
								"os_name":      schema.StringAttribute{Optional: true, MarkdownDescription: "OS, e.g. `ubuntu`."},
							},
						},
						"scale_group": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `virtual-machine-scale-group` components (a real ASG: launch template + autoscaling group).",
							Attributes: map[string]schema.Attribute{
								"architecture":     schema.StringAttribute{Optional: true, MarkdownDescription: "CPU architecture, e.g. `x86_64`."},
								"cpu":              schema.StringAttribute{Optional: true, MarkdownDescription: "vCPU count, e.g. `2`."},
								"ram":              schema.StringAttribute{Optional: true, MarkdownDescription: "RAM in GiB, e.g. `8`."},
								"os_name":          schema.StringAttribute{Optional: true, MarkdownDescription: "OS, e.g. `ubuntu`."},
								"min":              schema.Int64Attribute{Optional: true, MarkdownDescription: "Minimum instances."},
								"max":              schema.Int64Attribute{Optional: true, MarkdownDescription: "Maximum instances."},
								"desired":          schema.Int64Attribute{Optional: true, MarkdownDescription: "Desired instances."},
								"health":           schema.StringAttribute{Optional: true, MarkdownDescription: "Health check kind: `ec2` | `elb`."},
								"user_data":        schema.StringAttribute{Optional: true, MarkdownDescription: "cloud-init/bootstrap baked into the launch template (e.g. the native-binary pull)."},
								"instance_profile": schema.StringAttribute{Optional: true, MarkdownDescription: "IAM instance-profile name to attach (from a sibling `iam` component)."},
								"root_disk_gb":     schema.Int64Attribute{Optional: true, MarkdownDescription: "Root EBS volume size in GiB (0 = default)."},
							},
						},
						"attach_to_existing_alb": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for attaching a scale group to an existing ALB listener.",
							Attributes: map[string]schema.Attribute{
								"alb_listener_arn":  schema.StringAttribute{Required: true, MarkdownDescription: "ARN of the existing ALB listener."},
								"host_header":       schema.StringAttribute{Required: true, MarkdownDescription: "Host header rule to match."},
								"port":              schema.Int64Attribute{Required: true, MarkdownDescription: "Port the target group forwards traffic to."},
								"protocol":          schema.StringAttribute{Optional: true, MarkdownDescription: "Protocol (default `http`)."},
								"health_check_path": schema.StringAttribute{Optional: true, MarkdownDescription: "Health check path (default `/`)."},
								"health_check_port": schema.StringAttribute{Optional: true, MarkdownDescription: "Health check port (default is target group port)."},
								"scale_group":       schema.StringAttribute{Required: true, MarkdownDescription: "Name of the sibling scale-group component to attach."},
								"priority":          schema.Int64Attribute{Required: true, MarkdownDescription: "Unique priority for the listener rule."},
							},
						},
						"iam": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "IAM identity config for `iam` components (role + policies + instance profile).",
							Attributes: map[string]schema.Attribute{
								"assume_service":      schema.StringAttribute{Optional: true, MarkdownDescription: "Principal allowed to assume the role (default `ec2.amazonaws.com`)."},
								"managed_policy_arns": schema.ListAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: "Managed policy ARNs to attach."},
								"instance_profile":    schema.BoolAttribute{Optional: true, MarkdownDescription: "Also emit an instance profile (EC2 attach)."},
								"inline_policies": schema.ListNestedAttribute{
									Optional:            true,
									MarkdownDescription: "Inline policies (raw IAM JSON documents).",
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"name":     schema.StringAttribute{Required: true},
											"document": schema.StringAttribute{Required: true, MarkdownDescription: "Raw IAM policy JSON."},
										},
									},
								},
							},
						},
						"monitoring": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Monitoring config for `monitoring` components (CloudWatch log groups + metric alarms).",
							Attributes: map[string]schema.Attribute{
								"log_groups": schema.ListNestedAttribute{
									Optional: true,
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"name":           schema.StringAttribute{Required: true},
											"retention_days": schema.Int64Attribute{Optional: true, MarkdownDescription: "0 = never expire."},
										},
									},
								},
								"alarms": schema.ListNestedAttribute{
									Optional: true,
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"name":                schema.StringAttribute{Required: true},
											"namespace":           schema.StringAttribute{Required: true},
											"metric_name":         schema.StringAttribute{Required: true},
											"comparison_operator": schema.StringAttribute{Required: true},
											"threshold":           schema.Float64Attribute{Required: true},
											"evaluation_periods":  schema.Int64Attribute{Optional: true},
											"period_seconds":      schema.Int64Attribute{Optional: true},
											"statistic":           schema.StringAttribute{Optional: true},
										},
									},
								},
							},
						},
						"dns": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Cloudflare DNS config for `dns` components (cross-cutting; cloudflare_dns_record).",
							Attributes: map[string]schema.Attribute{
								"zone_id": schema.StringAttribute{Optional: true, MarkdownDescription: "Cloudflare zone id (else supplied via the cloudflare_zone_id tf var)."},
								"records": schema.ListNestedAttribute{
									Required: true,
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"name":    schema.StringAttribute{Required: true},
											"type":    schema.StringAttribute{Required: true, MarkdownDescription: "A | AAAA | CNAME | TXT | MX | ..."},
											"content": schema.StringAttribute{Required: true},
											"ttl":     schema.Int64Attribute{Optional: true, MarkdownDescription: "seconds; 1 = automatic."},
											"proxied": schema.BoolAttribute{Optional: true},
										},
									},
								},
							},
						},
						"object_storage": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `object-storage` / `blob-storage` components.",
							Attributes: map[string]schema.Attribute{
								"versioning": schema.BoolAttribute{Optional: true},
								"public":     schema.BoolAttribute{Optional: true, MarkdownDescription: "PUBLIC read (default false; opt-in only)."},
							},
						},
						"secrets": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `secrets-manager` components (the secret VALUE is set out of band, never here).",
							Attributes: map[string]schema.Attribute{
								"description":   schema.StringAttribute{Optional: true},
								"rotation_days": schema.Int64Attribute{Optional: true, MarkdownDescription: "0 = no automatic rotation."},
							},
						},
						"managed_database": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `managed-database` components (RDS/Cloud SQL/DO DB).",
							Attributes: map[string]schema.Attribute{
								"engine":     schema.StringAttribute{Optional: true, MarkdownDescription: "postgres | mysql."},
								"version":    schema.StringAttribute{Optional: true},
								"cpu":        schema.Int64Attribute{Optional: true},
								"ram":        schema.Int64Attribute{Optional: true},
								"storage_gb": schema.Int64Attribute{Optional: true},
								"ha":         schema.BoolAttribute{Optional: true},
								"encrypted":  schema.BoolAttribute{Optional: true},
							},
						},
						"queue": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `managed-queue` / `message-queue` components (SQS).",
							Attributes: map[string]schema.Attribute{
								"fifo":                       schema.BoolAttribute{Optional: true},
								"visibility_timeout_seconds": schema.Int64Attribute{Optional: true},
								"max_receive_count":          schema.Int64Attribute{Optional: true},
							},
						},
						"stream": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `event-streaming` / `event-bus` components (Kinesis).",
							Attributes: map[string]schema.Attribute{
								"shards":          schema.Int64Attribute{Optional: true},
								"retention_hours": schema.Int64Attribute{Optional: true},
							},
						},
						"serverless": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `serverless-function` components (Lambda).",
							Attributes: map[string]schema.Attribute{
								"runtime":         schema.StringAttribute{Optional: true},
								"runtime_version": schema.StringAttribute{Optional: true},
								"handler":         schema.StringAttribute{Optional: true},
								"memory_mb":       schema.Int64Attribute{Optional: true},
								"timeout_seconds": schema.Int64Attribute{Optional: true},
								"source_artifact": schema.StringAttribute{Optional: true},
							},
						},
						"kms": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `kms` / `encryption-key` components.",
							Attributes: map[string]schema.Attribute{
								"description":          schema.StringAttribute{Optional: true},
								"rotation_days":        schema.Int64Attribute{Optional: true},
								"deletion_window_days": schema.Int64Attribute{Optional: true},
							},
						},
						"cache": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"engine":    schema.StringAttribute{Optional: true},
								"version":   schema.StringAttribute{Optional: true},
								"memory_gb": schema.Int64Attribute{Optional: true},
								"ha":        schema.BoolAttribute{Optional: true},
							},
						},
						"cdn": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"origin_kind": schema.StringAttribute{Optional: true},
								"origin_name": schema.StringAttribute{Optional: true},
							},
						},
						"waf": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"scope":          schema.StringAttribute{Optional: true},
								"associate_name": schema.StringAttribute{Optional: true},
							},
						},
						"kubernetes": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"version":       schema.StringAttribute{Optional: true},
								"architecture":  schema.StringAttribute{Optional: true},
								"node_cpu":      schema.Int64Attribute{Optional: true},
								"node_ram":      schema.Int64Attribute{Optional: true},
								"min_nodes":     schema.Int64Attribute{Optional: true},
								"max_nodes":     schema.Int64Attribute{Optional: true},
								"desired_nodes": schema.Int64Attribute{Optional: true},
							},
						},
						"load_balancer": schema.SingleNestedAttribute{
							Optional: true,
							Attributes: map[string]schema.Attribute{
								"listeners": schema.ListNestedAttribute{
									Optional: true,
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"port":     schema.Int64Attribute{Required: true},
											"protocol": schema.StringAttribute{Required: true, MarkdownDescription: "http | https | tcp."},
										},
									},
								},
								"health_check_path": schema.StringAttribute{Optional: true},
								"health_check_port": schema.Int64Attribute{Optional: true},
								"health_protocol":   schema.StringAttribute{Optional: true},
								"stickiness":        schema.BoolAttribute{Optional: true},
								"target_kind":       schema.StringAttribute{Optional: true, MarkdownDescription: "vm | scale-group."},
								"target_name":       schema.StringAttribute{Optional: true},
							},
						},
						"email": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `email` / `email-service` components (AWS SES).",
							Attributes: map[string]schema.Attribute{
								"domain": schema.StringAttribute{Optional: true, MarkdownDescription: "Sending domain to verify."},
							},
						},
						"block_storage": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `block-storage` components (EBS/PD/Volume attached to a VM).",
							Attributes: map[string]schema.Attribute{
								"size_gb":     schema.Int64Attribute{Required: true},
								"volume_type": schema.StringAttribute{Optional: true},
								"device_name": schema.StringAttribute{Optional: true},
								"target_vm":   schema.StringAttribute{Required: true, MarkdownDescription: "VM component to attach to."},
							},
						},
						"prefix_list": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `prefix-list` components (AWS managed prefix list).",
							Attributes: map[string]schema.Attribute{
								"entries": schema.ListNestedAttribute{
									Required: true,
									NestedObject: schema.NestedAttributeObject{
										Attributes: map[string]schema.Attribute{
											"cidr":        schema.StringAttribute{Required: true},
											"description": schema.StringAttribute{Optional: true},
										},
									},
								},
							},
						},
						"synthetics": schema.SingleNestedAttribute{
							Optional:            true,
							MarkdownDescription: "Config for `synthetics` / `uptime-check` components.",
							Attributes: map[string]schema.Attribute{
								"target_url":      schema.StringAttribute{Optional: true},
								"runtime":         schema.StringAttribute{Optional: true},
								"handler":         schema.StringAttribute{Optional: true},
								"schedule_expr":   schema.StringAttribute{Optional: true},
								"artifact_bucket": schema.StringAttribute{Optional: true},
								"exec_role_arn":   schema.StringAttribute{Optional: true},
							},
						},
					},
				},
			},
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
		Name:     m.Name.ValueString(),
		Provider: m.Provider.ValueString(),
		Region:   m.Region.ValueString(),
	}
	for _, cm := range m.Components {
		count := int(cm.Count.ValueInt64())
		if count <= 0 {
			count = 1
		}
		comp := catalog.AssembleComponent{Name: cm.Name.ValueString(), Type: cm.Type.ValueString(), Count: count}
		if cm.VM != nil {
			comp.VM = &catalog.AssembleVM{
				Architecture: cm.VM.Architecture.ValueString(),
				CPU:          cm.VM.CPU.ValueString(),
				RAM:          cm.VM.RAM.ValueString(),
				OS:           cm.VM.OS.ValueString(),
			}
		}
		if cm.ScaleGroup != nil {
			comp.ScaleGroup = &catalog.AssembleScaleGroup{
				Architecture:    cm.ScaleGroup.Architecture.ValueString(),
				CPU:             cm.ScaleGroup.CPU.ValueString(),
				RAM:             cm.ScaleGroup.RAM.ValueString(),
				OS:              cm.ScaleGroup.OSName.ValueString(),
				Min:             int(cm.ScaleGroup.Min.ValueInt64()),
				Max:             int(cm.ScaleGroup.Max.ValueInt64()),
				Desired:         int(cm.ScaleGroup.Desired.ValueInt64()),
				Health:          cm.ScaleGroup.Health.ValueString(),
				UserData:        cm.ScaleGroup.UserData.ValueString(),
				InstanceProfile: cm.ScaleGroup.InstanceProfile.ValueString(),
				RootDiskGB:      int(cm.ScaleGroup.RootDiskGB.ValueInt64()),
			}
		}
		if cm.AttachToExistingALB != nil {
			comp.AttachToExistingALB = &catalog.AssembleAttachToExistingALB{
				ALBListenerARN:  cm.AttachToExistingALB.ALBListenerARN.ValueString(),
				HostHeader:      cm.AttachToExistingALB.HostHeader.ValueString(),
				Port:            int(cm.AttachToExistingALB.Port.ValueInt64()),
				Protocol:        cm.AttachToExistingALB.Protocol.ValueString(),
				HealthCheckPath: cm.AttachToExistingALB.HealthCheckPath.ValueString(),
				HealthCheckPort: cm.AttachToExistingALB.HealthCheckPort.ValueString(),
				ScaleGroup:      cm.AttachToExistingALB.ScaleGroup.ValueString(),
				Priority:        int(cm.AttachToExistingALB.Priority.ValueInt64()),
			}
		}
		if cm.IAM != nil {
			iam := &catalog.AssembleIAM{
				AssumeService:   cm.IAM.AssumeService.ValueString(),
				InstanceProfile: cm.IAM.InstanceProfile.ValueBool(),
			}
			for _, arn := range cm.IAM.ManagedPolicyARNs {
				iam.ManagedPolicyARNs = append(iam.ManagedPolicyARNs, arn.ValueString())
			}
			for _, p := range cm.IAM.InlinePolicies {
				iam.InlinePolicies = append(iam.InlinePolicies, catalog.IAMPolicy{
					Name: p.Name.ValueString(), Document: p.Document.ValueString(),
				})
			}
			comp.IAM = iam
		}
		if cm.Monitoring != nil {
			mon := &catalog.AssembleMonitoring{}
			for _, lg := range cm.Monitoring.LogGroups {
				mon.LogGroups = append(mon.LogGroups, catalog.LogGroup{
					Name: lg.Name.ValueString(), RetentionDays: int(lg.RetentionDays.ValueInt64()),
				})
			}
			for _, a := range cm.Monitoring.Alarms {
				mon.Alarms = append(mon.Alarms, catalog.MetricAlarm{
					Name: a.Name.ValueString(), Namespace: a.Namespace.ValueString(),
					MetricName: a.MetricName.ValueString(), ComparisonOperator: a.ComparisonOperator.ValueString(),
					Threshold: a.Threshold.ValueFloat64(), EvaluationPeriods: int(a.EvaluationPeriods.ValueInt64()),
					PeriodSeconds: int(a.PeriodSeconds.ValueInt64()), Statistic: a.Statistic.ValueString(),
				})
			}
			comp.Monitoring = mon
		}
		if cm.DNS != nil {
			dns := &catalog.AssembleDNS{ZoneID: cm.DNS.ZoneID.ValueString()}
			for _, r := range cm.DNS.Records {
				dns.Records = append(dns.Records, catalog.DNSRecord{
					Name: r.Name.ValueString(), Type: r.Type.ValueString(), Content: r.Content.ValueString(),
					TTL: int(r.TTL.ValueInt64()), Proxied: r.Proxied.ValueBool(),
				})
			}
			comp.DNS = dns
		}
		if cm.ObjectStorage != nil {
			comp.ObjectStorage = &catalog.AssembleObjectStorage{
				Versioning: cm.ObjectStorage.Versioning.ValueBool(),
				Public:     cm.ObjectStorage.Public.ValueBool(),
			}
		}
		if cm.Secrets != nil {
			comp.Secrets = &catalog.AssembleSecrets{
				Description:  cm.Secrets.Description.ValueString(),
				RotationDays: int(cm.Secrets.RotationDays.ValueInt64()),
			}
		}
		if cm.MDB != nil {
			comp.MDB = &catalog.AssembleMDB{
				Engine: cm.MDB.Engine.ValueString(), Version: cm.MDB.Version.ValueString(),
				CPU: int(cm.MDB.CPU.ValueInt64()), RAM: int(cm.MDB.RAM.ValueInt64()),
				StorageGB: int(cm.MDB.StorageGB.ValueInt64()), HA: cm.MDB.HA.ValueBool(),
				Encrypted: cm.MDB.Encrypted.ValueBool(),
			}
		}
		if cm.Queue != nil {
			comp.Queue = &catalog.AssembleQueue{
				FIFO: cm.Queue.FIFO.ValueBool(), VisibilityTimeoutSeconds: int(cm.Queue.VisibilityTimeoutSeconds.ValueInt64()),
				MaxReceiveCount: int(cm.Queue.MaxReceiveCount.ValueInt64()),
			}
		}
		if cm.Stream != nil {
			comp.Stream = &catalog.AssembleStream{
				Shards: int(cm.Stream.Shards.ValueInt64()), RetentionHours: int(cm.Stream.RetentionHours.ValueInt64()),
			}
		}
		if cm.Serverless != nil {
			comp.Serverless = &catalog.AssembleServerless{
				Runtime: cm.Serverless.Runtime.ValueString(), RuntimeVersion: cm.Serverless.RuntimeVersion.ValueString(),
				Handler: cm.Serverless.Handler.ValueString(), MemoryMB: int(cm.Serverless.MemoryMB.ValueInt64()),
				TimeoutSeconds: int(cm.Serverless.TimeoutSeconds.ValueInt64()), SourceArtifact: cm.Serverless.SourceArtifact.ValueString(),
			}
			if cm.KMS != nil {
				comp.KMS = &catalog.AssembleKMS{
					Description: cm.KMS.Description.ValueString(), RotationDays: int(cm.KMS.RotationDays.ValueInt64()),
					DeletionWindowDays: int(cm.KMS.DeletionWindowDays.ValueInt64()),
				}
			}
			if cm.Cache != nil {
				comp.Cache = &catalog.AssembleCache{Engine: cm.Cache.Engine.ValueString(), Version: cm.Cache.Version.ValueString(), MemoryGB: int(cm.Cache.MemoryGB.ValueInt64()), HA: cm.Cache.HA.ValueBool()}
			}
			if cm.CDN != nil {
				comp.CDN = &catalog.AssembleCDN{OriginKind: cm.CDN.OriginKind.ValueString(), OriginName: cm.CDN.OriginName.ValueString()}
			}
			if cm.WAF != nil {
				comp.WAF = &catalog.AssembleWAF{Scope: cm.WAF.Scope.ValueString(), AssociateName: cm.WAF.AssociateName.ValueString()}
			}
			if cm.K8s != nil {
				comp.K8s = &catalog.AssembleK8s{Version: cm.K8s.Version.ValueString(), Architecture: cm.K8s.Architecture.ValueString(), NodeCPU: int(cm.K8s.NodeCPU.ValueInt64()), NodeRAM: int(cm.K8s.NodeRAM.ValueInt64()), MinNodes: int(cm.K8s.MinNodes.ValueInt64()), MaxNodes: int(cm.K8s.MaxNodes.ValueInt64()), DesiredNodes: int(cm.K8s.DesiredNodes.ValueInt64())}
			}
			if cm.LB != nil {
				lb := &catalog.AssembleLB{HealthCheckPath: cm.LB.HealthCheckPath.ValueString(), HealthCheckPort: int(cm.LB.HealthCheckPort.ValueInt64()), HealthProtocol: cm.LB.HealthProtocol.ValueString(), Stickiness: cm.LB.Stickiness.ValueBool(), TargetKind: cm.LB.TargetKind.ValueString(), TargetName: cm.LB.TargetName.ValueString()}
				for _, l := range cm.LB.Listeners {
					lb.Listeners = append(lb.Listeners, catalog.AssembleLBListener{Port: int(l.Port.ValueInt64()), Protocol: l.Protocol.ValueString()})
				}
				comp.LB = lb
			}
			if cm.Email != nil {
				comp.Email = &catalog.AssembleEmail{Domain: cm.Email.Domain.ValueString()}
			}
			if cm.BlockStorage != nil {
				comp.BlockStorage = &catalog.AssembleBlockStorage{SizeGB: int(cm.BlockStorage.SizeGB.ValueInt64()), VolumeType: cm.BlockStorage.VolumeType.ValueString(), DeviceName: cm.BlockStorage.DeviceName.ValueString(), TargetVM: cm.BlockStorage.TargetVM.ValueString()}
			}
			if cm.PrefixList != nil {
				pl := &catalog.AssemblePrefixList{}
				for _, e := range cm.PrefixList.Entries {
					pl.Entries = append(pl.Entries, catalog.PrefixEntry{CIDR: e.CIDR.ValueString(), Description: e.Description.ValueString()})
				}
				comp.PrefixList = pl
			}
			if cm.Synthetics != nil {
				comp.Synthetics = &catalog.AssembleSynthetics{TargetURL: cm.Synthetics.TargetURL.ValueString(), Runtime: cm.Synthetics.Runtime.ValueString(), Handler: cm.Synthetics.Handler.ValueString(), ScheduleExpr: cm.Synthetics.ScheduleExpr.ValueString(), ArtifactBucket: cm.Synthetics.ArtifactBucket.ValueString(), ExecRoleARN: cm.Synthetics.ExecRoleARN.ValueString()}
			}
		}
		in.Components = append(in.Components, comp)
	}
	return in
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

// translateAndApply translates the topology LOCALLY (catalog.AssembleHCL — the
// same Translate/Render the round-trip harness uses, no backend round-trip and no
// token), then runs the resulting terraform in workDir with the ambient provider
// env credentials (Mode A).
func (r *environmentResource) translateAndApply(ctx context.Context, m *environmentModel) (map[string]string, string, error) {
	if m.modeB() {
		return nil, "", errModeBNotEnabled
	}
	if r.catalog == nil {
		return nil, "", fmt.Errorf("provider not configured: missing catalog")
	}
	docs, err := catalog.AssembleHCL(ctx, r.catalog, r.assembleInputFromModel(*m))
	if err != nil {
		return nil, "", fmt.Errorf("translate: %w", err)
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
