package provider

// resource_migration.go registers the pyxcloud_migration resource: the thin,
// opaque Terraform surface over the provider-side migration client
// (MIGRATION.md §2.1). It exposes ONLY the migration{} configuration block and
// the coarse, opacity-safe outputs (phase / percent / verdict / rollback /
// chosen substrate). It holds NO migration logic — that lives sealed on the
// backend. The resource drives internal/migration.Engine, which ferries a sealed
// opaque bundle to a confidential runtime and reports status.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/migration"
	migruntime "github.com/PyxCloud/terraform-provider-pyxcloud/internal/migration/runtime"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// migrationResource is the opaque migration resource.
type migrationResource struct {
	cfg migration.Config
}

var (
	_ resource.Resource              = (*migrationResource)(nil)
	_ resource.ResourceWithConfigure = (*migrationResource)(nil)
)

// NewMigrationResource is the framework resource factory.
func NewMigrationResource() resource.Resource { return &migrationResource{} }

// migrationBlockModel maps the migration{} configuration block (MIGRATION.md §2.1).
type migrationBlockModel struct {
	Enabled             types.Bool   `tfsdk:"enabled"`
	ConfidentialRuntime types.String `tfsdk:"confidential_runtime"`
	AttestationEndpoint types.String `tfsdk:"attestation_endpoint"`
	MaxDuration         types.String `tfsdk:"max_duration"`
	DryRun              types.Bool   `tfsdk:"dry_run"`
}

// migrationModel maps the pyxcloud_migration resource state.
type migrationModel struct {
	ID             types.String         `tfsdk:"id"`
	Place          types.String         `tfsdk:"place"`
	SourceProvider types.String         `tfsdk:"source_provider"`
	TargetProvider types.String         `tfsdk:"target_provider"`
	SourceTopology types.String         `tfsdk:"source_topology"`
	Migration      *migrationBlockModel `tfsdk:"migration"`

	// Computed, opacity-safe outputs (the runner's only visibility).
	RunID          types.String `tfsdk:"run_id"`
	Substrate      types.String `tfsdk:"substrate"`
	HardwareBacked types.Bool   `tfsdk:"hardware_backed"`
	RuntimeDetail  types.String `tfsdk:"runtime_detail"`
	Phase          types.String `tfsdk:"phase"`
	Percent        types.Int64  `tfsdk:"percent"`
	Verdict        types.String `tfsdk:"verdict"`
	RolledBack     types.Bool   `tfsdk:"rolled_back"`
	AttestationOK  types.Bool   `tfsdk:"attestation_ok"`
}

func (r *migrationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_migration"
}

func (r *migrationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Provider→provider migration of a PyxCloud macro place. A THIN, " +
			"OPAQUE client: it ferries a sealed (encrypted) execution bundle from the PyxCloud " +
			"backend to a confidential runtime and reports coarse status. The migration logic " +
			"(CRIU / rsync / DB / secret / queue / DNS sequencing) is a backend industrial secret " +
			"sealed inside the bundle and NEVER present in the provider — the provider only ever " +
			"holds ciphertext + coarse phase/percent/verdict.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Server-assigned migration run identifier.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"place": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "The macro logical place being migrated.",
			},
			"source_provider": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Current deployment provider for the place.",
			},
			"target_provider": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Provider to migrate the place to (the chosen-provider switch).",
			},
			"source_topology": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Canonical topology JSON for the place. Forwarded opaquely to " +
					"the backend planner; the provider does not interpret it for migration purposes.",
			},
			"migration": schema.SingleNestedAttribute{
				Required: true,
				MarkdownDescription: "Migration controls. The provider exposes ONLY these knobs — no " +
					"migration steps, scripts, or ordering live in the provider source.",
				Attributes: map[string]schema.Attribute{
					"enabled": schema.BoolAttribute{
						Required:            true,
						MarkdownDescription: "Whether the migration is active.",
					},
					"confidential_runtime": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Confidential-runtime substrate: `auto` (default — detect " +
							"the strongest available: confidential-container → hardware-TEE → " +
							"sealed-WASM floor), `local-tee`, or `confidential-container`. The floor is " +
							"always a memory-sealed sandbox; plaintext is never exposed to the host.",
					},
					"attestation_endpoint": schema.StringAttribute{
						Optional: true,
						MarkdownDescription: "Attestation root the runtime/backend use. Forwarded; the " +
							"provider does not itself verify attestation.",
					},
					"max_duration": schema.StringAttribute{
						Optional:            true,
						MarkdownDescription: "Max migration duration (Go duration, e.g. `2h`). Bounds the run.",
					},
					"dry_run": schema.BoolAttribute{
						Optional:            true,
						MarkdownDescription: "Plan/verify without performing the cutover.",
					},
				},
			},

			// Opacity-safe computed outputs.
			"run_id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Backend migration run id."},
			"substrate":       schema.StringAttribute{Computed: true, MarkdownDescription: "Confidential-runtime substrate actually chosen."},
			"hardware_backed": schema.BoolAttribute{Computed: true, MarkdownDescription: "True when the chosen substrate is hardware-isolated (TEE/CC)."},
			"runtime_detail":  schema.StringAttribute{Computed: true, MarkdownDescription: "Human note on substrate detection."},
			"phase":           schema.StringAttribute{Computed: true, MarkdownDescription: "Final coarse phase (never the step ordering)."},
			"percent":         schema.Int64Attribute{Computed: true, MarkdownDescription: "Final progress percentage."},
			"verdict":         schema.StringAttribute{Computed: true, MarkdownDescription: "`success` | `rolled-back` | `failed`."},
			"rolled_back":     schema.BoolAttribute{Computed: true, MarkdownDescription: "True if the migration rolled back (source preserved)."},
			"attestation_ok":  schema.BoolAttribute{Computed: true, MarkdownDescription: "True if the runtime produced attestation evidence."},
		},
	}
}

func (r *migrationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	pd, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("expected *providerData, got %T", req.ProviderData))
		return
	}
	r.cfg = pd.migration
}

func (r *migrationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan migrationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.runMigration(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *migrationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state migrationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// A completed migration run is terminal; nothing to refresh from the BE here.
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *migrationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan migrationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.runMigration(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *migrationResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	// A migration run is a completed action; removing it from state is a no-op
	// (it does not "undo" a converged cutover).
}

// runMigration drives the opaque engine and folds the opacity-safe result into
// state. It NEVER inspects the bundle or any plaintext.
func (r *migrationResource) runMigration(ctx context.Context, m *migrationModel, diags interface {
	AddError(string, string)
	HasError() bool
}) {
	if m.Migration == nil || !m.Migration.Enabled.ValueBool() {
		// Disabled migration: record a no-op pending state.
		m.ID = types.StringValue("migration-disabled")
		m.Phase = types.StringValue(string(migruntime.PhasePending))
		m.Substrate = types.StringValue("")
		m.RunID = types.StringValue("")
		m.Verdict = types.StringValue("")
		m.Percent = types.Int64Value(0)
		m.HardwareBacked = types.BoolValue(false)
		m.RuntimeDetail = types.StringValue("migration disabled")
		m.RolledBack = types.BoolValue(false)
		m.AttestationOK = types.BoolValue(false)
		return
	}

	opt := migration.Options{
		ConfidentialRuntime: substrateFromConfig(m.Migration.ConfidentialRuntime.ValueString()),
		AttestationEndpoint: m.Migration.AttestationEndpoint.ValueString(),
		DryRun:              m.Migration.DryRun.ValueBool(),
	}

	runCtx := ctx
	if d := m.Migration.MaxDuration.ValueString(); d != "" {
		if dur, err := time.ParseDuration(d); err == nil && dur > 0 {
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(ctx, dur)
			defer cancel()
		}
	}

	in := migration.PlanInput{
		Place:          m.Place.ValueString(),
		SourceProvider: m.SourceProvider.ValueString(),
		TargetProvider: m.TargetProvider.ValueString(),
		DryRun:         m.Migration.DryRun.ValueBool(),
	}
	if st := m.SourceTopology.ValueString(); st != "" {
		in.SourceTopology = json.RawMessage(st)
	}

	eng := migration.NewEngine(migration.NewClient(r.cfg))
	res, err := eng.Run(runCtx, in, opt)
	if err != nil {
		diags.AddError("Migration failed", err.Error())
		return
	}

	m.ID = types.StringValue(res.RunID)
	m.RunID = types.StringValue(res.RunID)
	m.Substrate = types.StringValue(string(res.Substrate))
	m.HardwareBacked = types.BoolValue(res.HardwareBacked)
	m.RuntimeDetail = types.StringValue(res.RuntimeDetail)
	m.Phase = types.StringValue(string(res.FinalPhase))
	m.Percent = types.Int64Value(int64(res.Percent))
	m.Verdict = types.StringValue(res.Verdict)
	m.RolledBack = types.BoolValue(res.RolledBack)
	m.AttestationOK = types.BoolValue(res.AttestationOK)
}

// substrateFromConfig maps the schema string to a runtime.Substrate, defaulting
// to auto.
func substrateFromConfig(s string) migruntime.Substrate {
	switch s {
	case "", string(migruntime.SubstrateAuto):
		return migruntime.SubstrateAuto
	case string(migruntime.SubstrateLocalTEE):
		return migruntime.SubstrateLocalTEE
	case string(migruntime.SubstrateConfidentialContainer):
		return migruntime.SubstrateConfidentialContainer
	default:
		return migruntime.Substrate(s)
	}
}
