package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

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
	ID              types.String        `tfsdk:"id"`
	Name            types.String        `tfsdk:"name"`
	Provider        types.String        `tfsdk:"cloud"`
	Region          types.String        `tfsdk:"region"`
	Components      []envComponentModel `tfsdk:"components"`
	AccountBinding  types.String        `tfsdk:"account_binding"`
	WorkDir         types.String        `tfsdk:"work_dir"`
	BackendS3Bucket types.String        `tfsdk:"backend_s3_bucket"`
	BackendS3Key    types.String        `tfsdk:"backend_s3_key"`
	BackendS3Region types.String        `tfsdk:"backend_s3_region"`
	Outputs         types.Map           `tfsdk:"outputs"`
}

// envComponentModel is the env-resource-specific component (decoupled from the
// shared topology componentModel so env-only blocks like iam don't churn the
// topology/compare schemas). VM sizing reuses the shared vmTypeModel.
type envComponentModel struct {
	Path                     types.String          `tfsdk:"path"`
	Name                     types.String          `tfsdk:"name"`
	Type                     types.String          `tfsdk:"type"`
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
	ALBListenerARN           types.String          `tfsdk:"alb_listener_arn"`
	HostHeader               types.String          `tfsdk:"host_header"`
	Port                     types.Int64           `tfsdk:"port"`
	Protocol                 types.String          `tfsdk:"protocol"`
	HealthCheckPath          types.String          `tfsdk:"health_check_path"`
	HealthCheckPortString    types.String          `tfsdk:"health_check_port"`
	ScaleGroupName           types.String          `tfsdk:"scale_group"`
	Priority                 types.Int64           `tfsdk:"priority"`
	AssumeService            types.String          `tfsdk:"assume_service"`
	CreateInstanceProfile    types.Bool            `tfsdk:"create_instance_profile"`
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
				MarkdownDescription: "Canonical components that make up the environment. Component properties are flat at this level; nested type-specific blocks are not exposed.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: flatEnvironmentComponentAttributes(),
				},
			},
			"work_dir": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Directory where the translated terraform runs and its state lives. " +
					"Must be stable for the resource lifecycle; defaults to `${cwd}/.pyxcloud/environments/<name>`.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"backend_s3_bucket": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional S3 bucket for the translated child Terraform state. Set with backend_s3_key/backend_s3_region for durable CI applies.",
			},
			"backend_s3_key": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional S3 key for the translated child Terraform state.",
			},
			"backend_s3_region": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional AWS region for the translated child Terraform S3 backend.",
			},
			"outputs": schema.MapAttribute{
				Computed:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Terraform outputs from the applied environment.",
			},
		},
	}
}

func flatEnvironmentComponentAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"path":         schema.StringAttribute{Optional: true, MarkdownDescription: "Canonical topology path for this component, e.g. `/0/Europe/0/Web-Net/0/app`."},
		"name":         schema.StringAttribute{Required: true, MarkdownDescription: "Component name."},
		"type":         schema.StringAttribute{Required: true, MarkdownDescription: "Canonical component type, e.g. `virtual-machine`, `managed-database`, `load-balancer`, `object-storage`."},
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
		"create_instance_profile":    schema.BoolAttribute{Optional: true, MarkdownDescription: "For IAM components, also create an EC2 instance profile with the role name."},
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
		Name:     m.Name.ValueString(),
		Provider: m.Provider.ValueString(),
		Region:   m.Region.ValueString(),
	}
	for _, cm := range m.Components {
		count := int(cm.Count.ValueInt64())
		if count <= 0 {
			count = 1
		}
		comp := catalog.AssembleComponent{Path: cm.Path.ValueString(), Name: cm.Name.ValueString(), Type: cm.Type.ValueString(), Count: count}

		if cm.Type.ValueString() == "virtual-machine" || hasFlatVM(cm.Architecture, cm.CPU, cm.RAM, cm.OSName) {
			comp.VM = &catalog.AssembleVM{
				Architecture:    cm.Architecture.ValueString(),
				CPU:             cm.CPU.ValueString(),
				RAM:             cm.RAM.ValueString(),
				OS:              cm.OSName.ValueString(),
				UserData:        cm.UserData.ValueString(),
				InstanceProfile: cm.InstanceProfileName.ValueString(),
			}
		}
		if cm.Type.ValueString() == "virtual-machine-scale-group" || hasScaleGroupFields(cm) {
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
			}
		}
		if cm.Type.ValueString() == "attach-to-existing-alb" || nonEmptyString(cm.ALBListenerARN) || nonEmptyString(cm.HostHeader) || nonEmptyString(cm.ScaleGroupName) {
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
		if cm.Type.ValueString() == "iam" || nonEmptyString(cm.AssumeService) || boolSet(cm.CreateInstanceProfile) || len(cm.ManagedPolicyARNs) > 0 || len(cm.InlinePolicies) > 0 {
			iam := &catalog.AssembleIAM{
				AssumeService:   cm.AssumeService.ValueString(),
				InstanceProfile: cm.CreateInstanceProfile.ValueBool(),
			}
			for _, arn := range cm.ManagedPolicyARNs {
				iam.ManagedPolicyARNs = append(iam.ManagedPolicyARNs, arn.ValueString())
			}
			for _, p := range cm.InlinePolicies {
				iam.InlinePolicies = append(iam.InlinePolicies, catalog.IAMPolicy{Name: p.Name.ValueString(), Document: p.Document.ValueString()})
			}
			comp.IAM = iam
		}
		if cm.Type.ValueString() == "monitoring" || len(cm.LogGroups) > 0 || len(cm.Alarms) > 0 {
			mon := &catalog.AssembleMonitoring{}
			for _, lg := range cm.LogGroups {
				mon.LogGroups = append(mon.LogGroups, catalog.LogGroup{Name: lg.Name.ValueString(), RetentionDays: int(lg.RetentionDays.ValueInt64())})
			}
			for _, a := range cm.Alarms {
				mon.Alarms = append(mon.Alarms, catalog.MetricAlarm{Name: a.Name.ValueString(), Namespace: a.Namespace.ValueString(), MetricName: a.MetricName.ValueString(), ComparisonOperator: a.ComparisonOperator.ValueString(), Threshold: a.Threshold.ValueFloat64(), EvaluationPeriods: int(a.EvaluationPeriods.ValueInt64()), PeriodSeconds: int(a.PeriodSeconds.ValueInt64()), Statistic: a.Statistic.ValueString()})
			}
			comp.Monitoring = mon
		}
		if cm.Type.ValueString() == "dns" || nonEmptyString(cm.ZoneID) || len(cm.Records) > 0 {
			dns := &catalog.AssembleDNS{ZoneID: cm.ZoneID.ValueString()}
			for _, r := range cm.Records {
				dns.Records = append(dns.Records, catalog.DNSRecord{Name: r.Name.ValueString(), Type: r.Type.ValueString(), Content: r.Content.ValueString(), TTL: int(r.TTL.ValueInt64()), Proxied: r.Proxied.ValueBool()})
			}
			comp.DNS = dns
		}
		if cm.Type.ValueString() == "object-storage" || cm.Type.ValueString() == "blob-storage" || boolSet(cm.Versioning) || boolSet(cm.Public) {
			comp.ObjectStorage = &catalog.AssembleObjectStorage{Versioning: cm.Versioning.ValueBool(), Public: cm.Public.ValueBool()}
		}
		if cm.Type.ValueString() == "secrets-manager" || nonEmptyString(cm.Description) || intSet(cm.RotationDays) {
			comp.Secrets = &catalog.AssembleSecrets{Description: cm.Description.ValueString(), RotationDays: int(cm.RotationDays.ValueInt64())}
		}
		if cm.Type.ValueString() == "managed-database" || hasDatabaseFields(cm) {
			comp.MDB = &catalog.AssembleMDB{Engine: cm.Engine.ValueString(), Version: cm.Version.ValueString(), CPU: intFromString(cm.CPU), RAM: intFromString(cm.RAM), StorageGB: int(cm.StorageGB.ValueInt64()), HA: cm.HA.ValueBool(), Encrypted: cm.Encrypted.ValueBool()}
		}
		if cm.Type.ValueString() == "managed-queue" || cm.Type.ValueString() == "message-queue" || boolSet(cm.FIFO) || intSet(cm.VisibilityTimeoutSeconds) || intSet(cm.MaxReceiveCount) {
			comp.Queue = &catalog.AssembleQueue{FIFO: cm.FIFO.ValueBool(), VisibilityTimeoutSeconds: int(cm.VisibilityTimeoutSeconds.ValueInt64()), MaxReceiveCount: int(cm.MaxReceiveCount.ValueInt64())}
		}
		if cm.Type.ValueString() == "event-streaming" || cm.Type.ValueString() == "event-bus" || intSet(cm.Shards) || intSet(cm.RetentionHours) {
			comp.Stream = &catalog.AssembleStream{Shards: int(cm.Shards.ValueInt64()), RetentionHours: int(cm.RetentionHours.ValueInt64())}
		}
		if cm.Type.ValueString() == "serverless-function" || nonEmptyString(cm.Runtime) || nonEmptyString(cm.Handler) || nonEmptyString(cm.SourceArtifact) {
			comp.Serverless = &catalog.AssembleServerless{Runtime: cm.Runtime.ValueString(), RuntimeVersion: cm.RuntimeVersion.ValueString(), Handler: cm.Handler.ValueString(), MemoryMB: int(cm.MemoryMB.ValueInt64()), TimeoutSeconds: int(cm.TimeoutSeconds.ValueInt64()), SourceArtifact: cm.SourceArtifact.ValueString()}
		}
		if cm.Type.ValueString() == "kms" || cm.Type.ValueString() == "encryption-key" || intSet(cm.DeletionWindowDays) {
			comp.KMS = &catalog.AssembleKMS{Description: cm.Description.ValueString(), RotationDays: int(cm.RotationDays.ValueInt64()), DeletionWindowDays: int(cm.DeletionWindowDays.ValueInt64())}
		}
		if cm.Type.ValueString() == "cache" || intSet(cm.MemoryGB) {
			comp.Cache = &catalog.AssembleCache{Engine: cm.Engine.ValueString(), Version: cm.Version.ValueString(), MemoryGB: int(cm.MemoryGB.ValueInt64()), HA: cm.HA.ValueBool()}
		}
		if cm.Type.ValueString() == "cdn" || cm.Type.ValueString() == "cdn-service" || nonEmptyString(cm.OriginKind) || nonEmptyString(cm.OriginName) {
			comp.CDN = &catalog.AssembleCDN{OriginKind: cm.OriginKind.ValueString(), OriginName: cm.OriginName.ValueString()}
		}
		if cm.Type.ValueString() == "waf" || nonEmptyString(cm.Scope) || nonEmptyString(cm.AssociateName) {
			comp.WAF = &catalog.AssembleWAF{Scope: cm.Scope.ValueString(), AssociateName: cm.AssociateName.ValueString()}
		}
		if cm.Type.ValueString() == "kubernetes" || intSet(cm.NodeCPU) || intSet(cm.MinNodes) {
			comp.K8s = &catalog.AssembleK8s{Version: cm.Version.ValueString(), Architecture: cm.Architecture.ValueString(), NodeCPU: int(cm.NodeCPU.ValueInt64()), NodeRAM: int(cm.NodeRAM.ValueInt64()), MinNodes: int(cm.MinNodes.ValueInt64()), MaxNodes: int(cm.MaxNodes.ValueInt64()), DesiredNodes: int(cm.DesiredNodes.ValueInt64())}
		}
		if cm.Type.ValueString() == "load-balancer" || len(cm.Listeners) > 0 || nonEmptyString(cm.TargetKind) || nonEmptyString(cm.TargetName) {
			lb := &catalog.AssembleLB{HealthCheckPath: cm.HealthCheckPath.ValueString(), HealthCheckPort: intFromString(cm.HealthCheckPortString), HealthProtocol: cm.HealthProtocol.ValueString(), Stickiness: cm.Stickiness.ValueBool(), TargetKind: cm.TargetKind.ValueString(), TargetName: cm.TargetName.ValueString()}
			for _, l := range cm.Listeners {
				lb.Listeners = append(lb.Listeners, catalog.AssembleLBListener{Port: int(l.Port.ValueInt64()), Protocol: l.Protocol.ValueString()})
			}
			comp.LB = lb
		}
		if cm.Type.ValueString() == "email" || cm.Type.ValueString() == "email-service" || nonEmptyString(cm.Domain) {
			comp.Email = &catalog.AssembleEmail{Domain: cm.Domain.ValueString()}
		}
		if cm.Type.ValueString() == "block-storage" || intSet(cm.SizeGB) || nonEmptyString(cm.TargetVM) {
			comp.BlockStorage = &catalog.AssembleBlockStorage{SizeGB: int(cm.SizeGB.ValueInt64()), VolumeType: cm.VolumeType.ValueString(), DeviceName: cm.DeviceName.ValueString(), TargetVM: cm.TargetVM.ValueString()}
		}
		if cm.Type.ValueString() == "prefix-list" || len(cm.Entries) > 0 {
			pl := &catalog.AssemblePrefixList{}
			for _, e := range cm.Entries {
				pl.Entries = append(pl.Entries, catalog.PrefixEntry{CIDR: e.CIDR.ValueString(), Description: e.Description.ValueString()})
			}
			comp.PrefixList = pl
		}
		if cm.Type.ValueString() == "synthetics" || cm.Type.ValueString() == "uptime-check" || nonEmptyString(cm.TargetURL) || nonEmptyString(cm.ScheduleExpr) {
			comp.Synthetics = &catalog.AssembleSynthetics{TargetURL: cm.TargetURL.ValueString(), Runtime: cm.Runtime.ValueString(), Handler: cm.Handler.ValueString(), ScheduleExpr: cm.ScheduleExpr.ValueString(), ArtifactBucket: cm.ArtifactBucket.ValueString(), ExecRoleARN: cm.ExecRoleARN.ValueString()}
		}
		in.Components = append(in.Components, comp)
	}
	return in
}

func hasScaleGroupFields(cm envComponentModel) bool {
	return intSet(cm.Min) || intSet(cm.Max) || intSet(cm.Desired) || nonEmptyString(cm.Health) || nonEmptyString(cm.UserData) || nonEmptyString(cm.InstanceProfileName) || intSet(cm.RootDiskGB)
}

func hasDatabaseFields(cm envComponentModel) bool {
	return nonEmptyString(cm.Engine) || nonEmptyString(cm.Version) || intSet(cm.StorageGB) || boolSet(cm.HA) || boolSet(cm.Encrypted)
}

func intSet(v types.Int64) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueInt64() != 0
}

func boolSet(v types.Bool) bool {
	return !v.IsNull() && !v.IsUnknown() && v.ValueBool()
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

func environmentBackendDoc(m environmentModel) string {
	if !nonEmptyString(m.BackendS3Bucket) && !nonEmptyString(m.BackendS3Key) && !nonEmptyString(m.BackendS3Region) {
		return ""
	}
	if !nonEmptyString(m.BackendS3Bucket) || !nonEmptyString(m.BackendS3Key) || !nonEmptyString(m.BackendS3Region) {
		return ""
	}
	return fmt.Sprintf(`terraform {
  backend "s3" {
    bucket  = %q
    key     = %q
    region  = %q
    encrypt = true
  }
}
`, m.BackendS3Bucket.ValueString(), m.BackendS3Key.ValueString(), m.BackendS3Region.ValueString())
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
	if backendDoc := environmentBackendDoc(*m); backendDoc != "" {
		docs = append([]string{backendDoc}, docs...)
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
