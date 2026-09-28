package catalog

// Lane 196 (2026-09-27 provider expansion): fail-closed Hostinger support.
//
// Official-support verification (sources pinned 2026-09-27, see
// handoffs/2026-09-27-provider-expansion/agents/194-hostinger-provider-report.md):
//
//   - Hostinger ships an official Terraform provider,
//     `hostinger/terraform-provider-hostinger`, whose documented resource set
//     covers `hostinger_vps` (plan_id / data_center_id / template_id).
//
//   - `hostinger_vps` create is BILLING-CONNECTED (uses the account payment
//     method by default) and destroy is documented as a SUBSCRIPTION
//     CANCELLATION. PyxCloud therefore never renders it for autonomous apply:
//     the F-9 ephemeral-runner lifecycle (ADR-0011) would have a real billing
//     side effect. Render/plan-only integration requires an owner decision.
//
//   - The provider exposes NO region/flavor/image discovery API that PyxCloud
//     can drive through its canonical catalog contract (ResolveRegion /
//     ResolveSKU / ResolveImage), so a `virtual-machine` translation cannot be
//     verified against real sizing data — it is refused, not invented (SPEC §1:
//     a component with no catalog translation is a HARD error, never a silent
//     drop).
//
//   - Hostinger S3-compatible object storage has NO official source. It stays
//     classified `unverified`/`not-established` [G] (master-plan unknown #1);
//     a missing upstream service cannot be solved by a boolean flip, and this
//     file explicitly refuses `object-storage` instead of inventing one.
//
//   - The Hostinger API reference (docs.hostinger.com) has no FaaS/serverless
//     primitive; `serverless-function` is refused the same way.
//
// What stays preserved and untouched by this file: F-2 native systemd on the
// app VM, F-3 artifacts in the owner's cloud area, F-9 the ephemeral per-run
// Terraform runner (droplet passo-run, destroyed on success/failure/cancel).
// Hostinger is recorded as a PLAUSIBLE future cloud (the registry provider
// source is pinned below for required_providers completeness) whose concrete
// integration is gated, explicit and plan-time — exactly the closure condition
// "explicitly refused, with a typed reason" of the boardos plan.

// ProviderHostinger is the provider-facing name for Hostinger. Its official
// Terraform provider source is hostinger/terraform-provider-hostinger. The
// catalog csp token would be "hostinger" as well, but NO csp row is registered
// in providerToCSP: there is no verified region/sku/image catalog, so the
// canonical translate dispatch cannot resolve a Hostinger environment yet.
// Registration happens only together with real catalog rows (H01 gate).
const ProviderHostinger = "hostinger"

// hostingerTFProviderSource is the registry source of the official Hostinger
// Terraform provider (verified 2026-09-27 via its docs/resources/vps.md).
const hostingerTFProviderSource = "hostinger/terraform-provider-hostinger"

// hostingerUnsupported builds the typed refusal for a Hostinger component.
// CSP/CSPRegion stay empty on purpose: without a registered csp row nothing was
// resolved, and fabricating a token would be worse than "(unresolved)".
func hostingerUnsupported(component, alternative string) ErrComponentUnsupported {
	return ErrComponentUnsupported{
		Component:   component,
		Provider:    ProviderHostinger,
		CSP:         "(no verified catalog row)",
		Alternative: alternative,
	}
}

// TranslateVMHostingerGuard is the explicit fail-closed gate for canonical
// `virtual-machine` components aimed at Hostinger. It is called before any
// catalog resolution would run, so the error names the real blocker:
//
//  1. no Hostinger region/sku/image discovery API is wired into the PyxCloud
//     catalog contract (H01 gate needs a real BillingCatalogApi fixture);
//  2. hostinger_vps create/destroy is a billing event (subscription), which
//     conflicts with the autonomous ephemeral-runner lifecycle without an
//     explicit owner decision.
func TranslateVMHostingerGuard() error {
	return hostingerUnsupported(TypeVirtualMachine,
		"Hostinger's official Terraform provider (hostinger/terraform-provider-hostinger, "+
			"pinned 2026-09-27) exposes only `hostinger_vps` (plan_id/data_center_id/template_id): "+
			"no region/flavor/image discovery API exists for PyxCloud's ResolveSKU/ResolveImage contract, "+
			"and hostinger_vps create is billing-connected while destroy is a subscription cancellation — "+
			"direct autonomous lifecycle is out of scope until the H01 catalog gate and an owner billing "+
			"decision land. Until then: use digitalocean_droplet (DEP-01.12 sizes) or another registered "+
			"wave-1/wave-2 provider; native systemd on the app VM (F-2) is unaffected by this refusal.")
}

// TranslateObjectStorageHostingerGuard is the explicit fail-closed gate for
// `object-storage` components aimed at Hostinger. There is NO official Hostinger
// S3-compatible storage source; the classification stays unverified/not-established [G]
// (never silently dropped, never invented as a Terraform resource).
func TranslateObjectStorageHostingerGuard() error {
	return hostingerUnsupported(TypeObjectStorage,
		"No official Hostinger S3-compatible object-storage source exists (checked 2026-09-27, "+
			"master-plan unknown #1): the service stays unverified/not-established [G] and PyxCloud does "+
			"not invent a Terraform resource for it. Use Cloudflare R2 (S3 API subset, provider v4/v5 per "+
			"I02 rules) or DigitalOcean Spaces instead; revisit only when an official Hostinger source lands.")
}

// TranslateServerlessHostingerGuard is the explicit fail-closed gate for
// `serverless-function` components aimed at Hostinger: the official Hostinger
// Terraform provider has no FaaS primitive.
func TranslateServerlessHostingerGuard() error {
	return hostingerUnsupported(TypeServerlessFunction,
		"Hostinger has no serverless/FaaS primitive in its official Terraform provider "+
			"(verified 2026-09-27). Use AWS Lambda, GCP Cloud Functions or the DigitalOcean App Platform "+
			"functions component instead.")
}
