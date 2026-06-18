package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/client"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type importTopologyResource struct {
	client client.Client
}

var (
	_ resource.Resource              = (*importTopologyResource)(nil)
	_ resource.ResourceWithConfigure = (*importTopologyResource)(nil)
)

func NewImportTopologyResource() resource.Resource {
	return &importTopologyResource{}
}

type importTopologyModel struct {
	ID                    types.String   `tfsdk:"id"`
	AccountBinding        types.String   `tfsdk:"account_binding"`
	Intent                types.String   `tfsdk:"intent"`
	SourceCloud           types.String   `tfsdk:"source_cloud"`
	SourceRegion          types.String   `tfsdk:"source_region"`
	TargetCloud           types.String   `tfsdk:"target_cloud"`
	TargetRegion          types.String   `tfsdk:"target_region"`
	SelectedResourceIDs   []types.String `tfsdk:"selected_resource_ids"`
	SelectedResourceTypes []types.String `tfsdk:"selected_resource_types"`
	MigrationFeeToken     types.String   `tfsdk:"migration_fee_token"`
	CanonicalTopology     types.String   `tfsdk:"canonical_topology"`
	RenderedTerraform     types.String   `tfsdk:"rendered_terraform"`
	FeeRequired           types.Bool     `tfsdk:"fee_required"`
	FeePaid               types.Bool     `tfsdk:"fee_paid"`
	FeeReason             types.String   `tfsdk:"fee_reason"`
	CheckoutURL           types.String   `tfsdk:"checkout_url"`
}

func (r *importTopologyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_import_topology"
}

func (r *importTopologyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Backend-gated import topology scaffold. It uses a PyxCloud account binding, " +
			"not raw cloud credentials, and can return observability-only or deployable topology output.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Provider-local stable identifier for this import topology request.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"account_binding": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "PyxCloud account binding identifier. Raw cloud credentials are not accepted by this provider surface.",
			},
			"intent": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "`observability` for read-only topology context or `deployable_topology` for backend-rendered import output.",
			},
			"source_cloud": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Source cloud for discovered resources.",
			},
			"source_region": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Source cloud region for discovered resources.",
			},
			"target_cloud": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Target deployment cloud for deployable topology output.",
			},
			"target_region": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Target PyxCloud macro-region or backend-supported region label.",
			},
			"selected_resource_ids": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Source resource identifiers selected for import.",
			},
			"selected_resource_types": schema.ListAttribute{
				Optional:            true,
				ElementType:         types.StringType,
				MarkdownDescription: "Source resource types selected for import.",
			},
			"migration_fee_token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "Optional backend checkout/entitlement token for fee-gated deployable imports. " +
					"Do not use raw cloud credentials here.",
			},
			"canonical_topology": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backend canonical topology as compact JSON.",
			},
			"rendered_terraform": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backend-rendered Terraform JSON as compact JSON when available.",
			},
			"fee_required": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "True when the backend requires payment before producing deployable topology output.",
			},
			"fee_paid": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "True when the backend accepted the supplied fee token or otherwise marked the request paid.",
			},
			"fee_reason": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backend explanation for a fee gate, if any.",
			},
			"checkout_url": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Backend checkout URL to obtain a migration fee token, if required.",
			},
		},
	}
}

func (r *importTopologyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
}

func (r *importTopologyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan importTopologyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.importTopology(ctx, plan)
	if err != nil {
		appendImportTopologyError(&resp.Diagnostics, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *importTopologyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state importTopologyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.MigrationFeeToken = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *importTopologyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan importTopologyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.importTopology(ctx, plan)
	if err != nil {
		appendImportTopologyError(&resp.Diagnostics, err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *importTopologyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.State.RemoveResource(ctx)
}

func (r *importTopologyResource) importTopology(ctx context.Context, plan importTopologyModel) (importTopologyModel, error) {
	out, err := r.client.ImportTopology(ctx, client.ImportTopologyRequest{
		AccountBinding:        plan.AccountBinding.ValueString(),
		Intent:                plan.Intent.ValueString(),
		SourceCloud:           optionalString(plan.SourceCloud),
		SourceRegion:          optionalString(plan.SourceRegion),
		TargetCloud:           optionalString(plan.TargetCloud),
		TargetRegion:          optionalString(plan.TargetRegion),
		SelectedResourceIDs:   stringsFromTypeList(plan.SelectedResourceIDs),
		SelectedResourceTypes: stringsFromTypeList(plan.SelectedResourceTypes),
		MigrationFeeToken:     optionalString(plan.MigrationFeeToken),
	})
	if err != nil {
		return importTopologyModel{}, err
	}

	state := plan
	state.ID = types.StringValue(importTopologyID(plan))
	state.MigrationFeeToken = types.StringNull()
	state.CanonicalTopology = types.StringValue(out.CanonicalTopologyJSON())
	state.RenderedTerraform = types.StringValue(out.RenderedTerraformJSON())
	state.FeeRequired = types.BoolValue(out.FeeRequired)
	state.FeePaid = types.BoolValue(out.FeePaid)
	state.FeeReason = types.StringValue(out.FeeReason)
	state.CheckoutURL = types.StringValue(out.CheckoutURL)
	return state, nil
}

func importTopologyID(m importTopologyModel) string {
	h := sha256.New()
	for _, part := range []string{
		m.AccountBinding.ValueString(),
		m.Intent.ValueString(),
		optionalString(m.SourceCloud),
		optionalString(m.SourceRegion),
		optionalString(m.TargetCloud),
		optionalString(m.TargetRegion),
	} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	for _, part := range stringsFromTypeList(m.SelectedResourceIDs) {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	for _, part := range stringsFromTypeList(m.SelectedResourceTypes) {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return "import-topology-" + hex.EncodeToString(h.Sum(nil))[:16]
}

type feeRequiredDiagnostic interface {
	error
	FeeReason() string
	Checkout() string
	BackendStatus() int
}

func appendImportTopologyError(diags *diag.Diagnostics, err error) {
	var feeErr feeRequiredDiagnostic
	if errors.As(err, &feeErr) {
		diags.Append(importTopologyFeeRequiredDiagnostic(feeErr))
		return
	}
	diags.AddError("Import topology failed", err.Error())
}

func importTopologyFeeRequiredDiagnostic(err feeRequiredDiagnostic) diag.Diagnostic {
	reason := err.FeeReason()
	if reason == "" {
		reason = err.Error()
	}
	detail := "The backend requires a migration fee before returning deployable import/provider-region-change topology output."
	if reason != "" {
		detail += " Reason: " + reason + "."
	}
	if checkout := err.Checkout(); checkout != "" {
		detail += " Complete checkout at " + checkout + " and rerun with migration_fee_token."
	} else {
		detail += " Obtain a migration_fee_token from PyxCloud checkout and rerun."
	}
	return diag.NewErrorDiagnostic("Migration fee required", detail)
}
