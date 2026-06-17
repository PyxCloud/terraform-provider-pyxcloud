package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// environmentResource is Mode A of DEPLOY-GATE.md: `terraform apply` of a
// pyxcloud_environment turns a canonical topology into a REAL environment by
// translating it to concrete provider terraform LOCALLY (catalog.AssembleHCL) and
// running it with the ambient provider env credentials (no accountBinding, no
// backend round-trip, no token). The terraform-native replacement for the
// per-provider scripts.
type environmentResource struct {
	cat catalog.Catalog
}

var (
	_ resource.Resource              = (*environmentResource)(nil)
	_ resource.ResourceWithConfigure = (*environmentResource)(nil)
)

// NewEnvironmentResource is the framework resource factory.
func NewEnvironmentResource() resource.Resource {
	return &environmentResource{}
}

// ---- model -----------------------------------------------------------------

type envNetworkModel struct {
	CIDR    types.String   `tfsdk:"cidr"`
	Subnets []types.String `tfsdk:"subnets"`
}

type envRuleModel struct {
	Direction types.String   `tfsdk:"direction"`
	Protocol  types.String   `tfsdk:"protocol"`
	FromPort  types.Int64    `tfsdk:"from_port"`
	ToPort    types.Int64    `tfsdk:"to_port"`
	CIDRs     []types.String `tfsdk:"cidrs"`
	SourceSG  types.String   `tfsdk:"source_sg"`
}

type envSGModel struct {
	Description types.String   `tfsdk:"description"`
	Expose      []types.Int64  `tfsdk:"expose"`
	Rules       []envRuleModel `tfsdk:"rules"`
}

type envVMModel struct {
	Architecture    types.String `tfsdk:"architecture"`
	CPU             types.Int64  `tfsdk:"cpu"`
	RAM             types.Int64  `tfsdk:"ram"`
	OS              types.String `tfsdk:"os"`
	OSVersion       types.String `tfsdk:"os_version"`
	Count           types.Int64  `tfsdk:"count"`
	UserData        types.String `tfsdk:"user_data"`
	InstanceProfile types.String `tfsdk:"instance_profile"`
}

type envIAMPolicyModel struct {
	Name     types.String `tfsdk:"name"`
	Document types.String `tfsdk:"document"`
}

type envIAMModel struct {
	Name              types.String        `tfsdk:"name"`
	AssumeService     types.String        `tfsdk:"assume_service"`
	InstanceProfile   types.Bool          `tfsdk:"instance_profile"`
	InlinePolicies    []envIAMPolicyModel `tfsdk:"inline_policies"`
	ManagedPolicyARNs []types.String      `tfsdk:"managed_policy_arns"`
}

type environmentModel struct {
	ID             types.String     `tfsdk:"id"`
	Name           types.String     `tfsdk:"name"`
	Provider       types.String     `tfsdk:"provider"`
	Region         types.String     `tfsdk:"region"`
	Network        *envNetworkModel `tfsdk:"network"`
	SecurityGroup  *envSGModel      `tfsdk:"security_group"`
	VirtualMachine *envVMModel      `tfsdk:"virtual_machine"`
	IAM            []envIAMModel    `tfsdk:"iam"`
	AccountBinding types.String     `tfsdk:"account_binding"`
	WorkDir        types.String     `tfsdk:"work_dir"`
	Outputs        types.Map        `tfsdk:"outputs"`
}

func (m environmentModel) modeB() bool {
	return !m.AccountBinding.IsNull() && !m.AccountBinding.IsUnknown() && m.AccountBinding.ValueString() != ""
}

// ---- framework plumbing -----------------------------------------------------

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Provisions a REAL environment from a canonical topology by translating it to " +
			"concrete provider terraform locally (catalog.AssembleHCL) and applying it with your ambient provider " +
			"env credentials (AWS_*, GOOGLE_*, DIGITALOCEAN_TOKEN). The terraform-native replacement for hand-written " +
			"per-provider terraform — no accountBinding, no backend-held creds, no token.",
		Attributes: map[string]schema.Attribute{
			"id":       schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"name":     schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
			"provider": schema.StringAttribute{Required: true, MarkdownDescription: "aws | gcp | digitalocean."},
			"region":   schema.StringAttribute{Required: true, MarkdownDescription: "Abstract pyx region_name (e.g. Dublin)."},
			"network": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"cidr":    schema.StringAttribute{Required: true},
					"subnets": schema.ListAttribute{Required: true, ElementType: types.StringType},
				},
			},
			"security_group": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"description": schema.StringAttribute{Optional: true},
					"expose":      schema.ListAttribute{Optional: true, ElementType: types.Int64Type, MarkdownDescription: "TCP ports opened ingress from anywhere."},
					"rules": schema.ListNestedAttribute{
						Optional: true,
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"direction": schema.StringAttribute{Required: true, MarkdownDescription: "ingress | egress"},
							"protocol":  schema.StringAttribute{Required: true, MarkdownDescription: "tcp | udp | icmp | all"},
							"from_port": schema.Int64Attribute{Optional: true},
							"to_port":   schema.Int64Attribute{Optional: true},
							"cidrs":     schema.ListAttribute{Optional: true, ElementType: types.StringType},
							"source_sg": schema.StringAttribute{Optional: true},
						}},
					},
				},
			},
			"virtual_machine": schema.SingleNestedAttribute{
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"architecture":     schema.StringAttribute{Optional: true},
					"cpu":              schema.Int64Attribute{Required: true},
					"ram":              schema.Int64Attribute{Required: true},
					"os":               schema.StringAttribute{Optional: true},
					"os_version":       schema.StringAttribute{Optional: true},
					"count":            schema.Int64Attribute{Optional: true},
					"user_data":        schema.StringAttribute{Optional: true, MarkdownDescription: "cloud-init/bootstrap script."},
					"instance_profile": schema.StringAttribute{Optional: true, MarkdownDescription: "IAM instance-profile name (from an iam block)."},
				},
			},
			"iam": schema.ListNestedAttribute{
				Optional: true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":             schema.StringAttribute{Required: true},
					"assume_service":   schema.StringAttribute{Optional: true, MarkdownDescription: "Trust principal (default ec2.amazonaws.com)."},
					"instance_profile": schema.BoolAttribute{Optional: true, MarkdownDescription: "Emit an instance-profile wrapping the role."},
					"inline_policies": schema.ListNestedAttribute{
						Optional: true,
						NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
							"name":     schema.StringAttribute{Required: true},
							"document": schema.StringAttribute{Required: true, MarkdownDescription: "IAM policy JSON (verbatim)."},
						}},
					},
					"managed_policy_arns": schema.ListAttribute{Optional: true, ElementType: types.StringType},
				}},
			},
			"account_binding": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Set to select Mode B (managed account, server-side deploy). Omit for Mode A (local env-credential apply).",
			},
			"work_dir": schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"outputs":  schema.MapAttribute{Computed: true, ElementType: types.StringType},
		},
	}
}

func (r *environmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *providerData, got %T", req.ProviderData))
		return
	}
	r.cat = pd.catalog
}

// ---- topology assembly ------------------------------------------------------

func strs(in []types.String) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, s.ValueString())
	}
	return out
}

// buildTopology maps the resource model into a catalog.Topology, propagating the
// environment-level provider/region onto each catalog Spec.
func (r *environmentResource) buildTopology(m *environmentModel) catalog.Topology {
	provider := m.Provider.ValueString()
	region := m.Region.ValueString()
	name := m.Name.ValueString()
	var topo catalog.Topology

	if m.Network != nil {
		topo.Network = &catalog.NetworkSpec{
			Name: name, Region: region, Provider: provider,
			CIDR: m.Network.CIDR.ValueString(), Subnets: strs(m.Network.Subnets),
		}
	}
	if m.SecurityGroup != nil {
		sg := &catalog.SecurityGroupSpec{
			Name: name, Network: name, Region: region, Provider: provider,
			Description: m.SecurityGroup.Description.ValueString(),
		}
		for _, p := range m.SecurityGroup.Expose {
			sg.Expose = append(sg.Expose, int(p.ValueInt64()))
		}
		for _, rule := range m.SecurityGroup.Rules {
			sg.Rules = append(sg.Rules, catalog.SecurityRule{
				Direction: rule.Direction.ValueString(),
				Protocol:  rule.Protocol.ValueString(),
				FromPort:  int(rule.FromPort.ValueInt64()),
				ToPort:    int(rule.ToPort.ValueInt64()),
				CIDRs:     strs(rule.CIDRs),
				SourceSG:  rule.SourceSG.ValueString(),
			})
		}
		topo.SecurityGroup = sg
	}
	if m.VirtualMachine != nil {
		vm := m.VirtualMachine
		topo.VirtualMachine = &catalog.VMSpec{
			Name: name, Region: region, Provider: provider,
			Architecture:    vm.Architecture.ValueString(),
			CPU:             int(vm.CPU.ValueInt64()),
			RAM:             int(vm.RAM.ValueInt64()),
			OS:              vm.OS.ValueString(),
			OSVersion:       vm.OSVersion.ValueString(),
			Count:           int(vm.Count.ValueInt64()),
			UserData:        vm.UserData.ValueString(),
			InstanceProfile: vm.InstanceProfile.ValueString(),
		}
		if m.Network != nil {
			topo.VirtualMachine.Network = name
			if len(m.Network.Subnets) > 0 {
				topo.VirtualMachine.Subnet = name + "-1"
			}
		}
		if m.SecurityGroup != nil {
			topo.VirtualMachine.SecurityGroup = name
		}
	}
	for _, im := range m.IAM {
		spec := catalog.IAMSpec{
			Name: im.Name.ValueString(), Provider: provider,
			AssumeService:   im.AssumeService.ValueString(),
			InstanceProfile: im.InstanceProfile.ValueBool(),
		}
		for _, p := range im.InlinePolicies {
			spec.InlinePolicies = append(spec.InlinePolicies, catalog.IAMPolicyDoc{
				Name: p.Name.ValueString(), Document: p.Document.ValueString(),
			})
		}
		spec.ManagedPolicyARNs = strs(im.ManagedPolicyARNs)
		topo.IAM = append(topo.IAM, spec)
	}
	return topo
}

// providerHeader emits the terraform{}+provider{} blocks. The provider reads its
// credentials from the ambient env (no inline creds), so a connected runner/CI
// authenticates exactly like the per-provider scripts do today.
func providerHeader(provider string) string {
	switch provider {
	case "gcp":
		return "terraform {\n  required_providers {\n    google = { source = \"hashicorp/google\" }\n  }\n}\nprovider \"google\" {}\n"
	case "digitalocean":
		return "terraform {\n  required_providers {\n    digitalocean = { source = \"digitalocean/digitalocean\" }\n  }\n}\nprovider \"digitalocean\" {}\n"
	default:
		return "terraform {\n  required_providers {\n    aws = { source = \"hashicorp/aws\" }\n  }\n}\nprovider \"aws\" {}\n"
	}
}

// translateAndApply (Mode A) assembles the topology to concrete .tf LOCALLY and
// runs it with the ambient env credentials.
func (r *environmentResource) translateAndApply(ctx context.Context, m *environmentModel) (map[string]string, string, error) {
	if m.modeB() {
		return nil, "", errModeBNotEnabled
	}
	topo := r.buildTopology(m)
	hcl, err := catalog.AssembleHCL(ctx, r.cat, topo)
	if err != nil {
		return nil, "", fmt.Errorf("local translation: %w", err)
	}
	workDir, err := r.resolveWorkDir(m)
	if err != nil {
		return nil, "", err
	}
	runner, err := newTFRunner(workDir)
	if err != nil {
		return nil, "", err
	}
	docs := []string{providerHeader(m.Provider.ValueString()), hcl}
	outputs, err := runner.apply(ctx, docs)
	if err != nil {
		return nil, workDir, err
	}
	return outputs, workDir, nil
}

var errModeBNotEnabled = fmt.Errorf("managed-account deploy (Mode B, account_binding set) is not yet enabled: " +
	"it runs server-side with the binding's stored credentials and requires the non-interactive deploy gate " +
	"(DEPLOY-GATE.md §B, pending). For now omit account_binding to use Mode A (apply locally with your ambient " +
	"provider env credentials)")

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

// ---- CRUD -------------------------------------------------------------------

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
		return // terraform unavailable — keep prior state
	}
	outputs, err := runner.refresh(ctx)
	if err != nil {
		return // best-effort refresh
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
