package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
	client client.Client
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
	ID         types.String     `tfsdk:"id"`
	Name       types.String     `tfsdk:"name"`
	Provider   types.String     `tfsdk:"provider"`
	Region     types.String     `tfsdk:"region"`
	Components []componentModel `tfsdk:"components"`
	WorkDir    types.String     `tfsdk:"work_dir"`
	Outputs    types.Map        `tfsdk:"outputs"`
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
			"provider": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Cloud provider to deploy to: `aws` | `gcp` | `digitalocean`.",
			},
			"region": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Abstract pyx region_name (e.g. `Dublin`); the backend resolves it to a concrete cspRegion.",
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
}

// topologyFromModel builds the canonical client.Topology from the resource model.
func (r *environmentResource) topologyFromModel(m environmentModel) client.Topology {
	topo := client.Topology{
		Name:     m.Name.ValueString(),
		Provider: m.Provider.ValueString(),
		Region:   m.Region.ValueString(),
	}
	for _, cm := range m.Components {
		count := int(cm.Count.ValueInt64())
		if count <= 0 {
			count = 1
		}
		comp := client.Component{Name: cm.Name.ValueString(), Type: cm.Type.ValueString(), Count: count}
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
	return topo
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

// translateAndApply asks the backend to translate the topology, then runs the
// resulting terraform in workDir with the ambient env credentials.
func (r *environmentResource) translateAndApply(ctx context.Context, m *environmentModel) (map[string]string, string, error) {
	tr, err := r.client.Translate(ctx, r.topologyFromModel(*m))
	if err != nil {
		return nil, "", fmt.Errorf("backend translate: %w", err)
	}
	if len(tr.Terraform) == 0 {
		return nil, "", fmt.Errorf("backend returned no terraform for this topology")
	}
	workDir, err := r.resolveWorkDir(m)
	if err != nil {
		return nil, "", err
	}
	runner, err := newTFRunner(workDir)
	if err != nil {
		return nil, "", err
	}
	outputs, err := runner.apply(ctx, tr.Terraform)
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
