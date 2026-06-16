package migration

// engine.go orchestrates one migration run on the provider side. It is pure
// glue + lifecycle: generate the ephemeral key → pick the confidential runtime →
// attest → request the sealed plan → hand the bundle to the runtime → poll coarse
// status → zeroize. It carries ZERO migration logic; every migration step lives
// sealed inside the bundle, executed only inside the runtime boundary.

import (
	"context"
	"fmt"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/migration/runtime"
)

// Options configures one migration run, mirroring the migration{} schema block.
type Options struct {
	// ConfidentialRuntime is "auto" (default), "local-tee", or
	// "confidential-container".
	ConfidentialRuntime runtime.Substrate
	// CCBackend selects the confidential-container backend when relevant
	// (empty = nominal Nitro).
	CCBackend runtime.CCBackend
	// AttestationEndpoint is the attestation root the runtime/BE use (forwarded;
	// the provider does not verify attestation itself).
	AttestationEndpoint string
	// DryRun plans/verifies without performing the cutover.
	DryRun bool
}

// Result is the opacity-safe outcome surfaced into Terraform state.
type Result struct {
	RunID          string
	Substrate      runtime.Substrate
	HardwareBacked bool
	RuntimeDetail  string
	FinalPhase     runtime.Phase
	Percent        int
	Verdict        string
	RolledBack     bool
	AttestationOK  bool
	// Observations is the coarse, opacity-safe phase trail the runner saw.
	Observations []runtime.Status
}

// Engine runs a migration through the opaque client + confidential runtime.
type Engine struct {
	client *Client
	// selectRuntime picks the confidential runtime for a run. Defaults to
	// runtime.Select; overridable in tests to inject a fake runtime.
	selectRuntime func(runtime.Substrate, runtime.Opener, runtime.CCBackend) (runtime.ConfidentialRuntime, runtime.Capability, error)
}

// NewEngine builds an engine over the opaque client.
func NewEngine(client *Client) *Engine {
	return &Engine{client: client, selectRuntime: runtime.Select}
}

// opener adapts the package-private HPKE open into the runtime.Opener the runtime
// uses INSIDE its sealed boundary. This is the ONLY place the parent package's
// crypto is handed to a runtime, and it returns plaintext only to in-seal code.
func opener(recipientPriv [32]byte, s runtime.Sealed, aad []byte) ([]byte, error) {
	return open(recipientPriv, Sealed{KEMPub: s.KEMPub, Ciphertext: s.Ciphertext}, aad)
}

// Run executes one migration run end to end and returns the opacity-safe Result.
// The ephemeral private key is zeroized before return on every path.
func (e *Engine) Run(ctx context.Context, in PlanInput, opt Options) (Result, error) {
	// 1. Per-run ephemeral keypair (private key in memory only).
	eph, err := newEphemeralKey()
	if err != nil {
		return Result{}, fmt.Errorf("migration: ephemeral key: %w", err)
	}
	defer eph.Zeroize() // zeroize on every exit path (§3.4)

	// 2. Select the confidential runtime (auto picks strongest available).
	rt, cape, err := e.selectRuntime(opt.ConfidentialRuntime, opener, opt.CCBackend)
	if err != nil {
		return Result{}, err
	}
	res := Result{
		Substrate:      cape.Substrate,
		HardwareBacked: cape.HardwareBacked,
		RuntimeDetail:  cape.Detail,
	}

	// 3. Bind the run's ephemeral public key into the attestation BEFORE attesting,
	//    so a confidential runtime (the real Nitro path) embeds it in the signed
	//    attestation document's public_key field and the backend verifier can bind
	//    the released key to THIS enclave + run (§3 steps 2-4). The
	//    ConfidentialRuntime interface deliberately does not carry the key, so this
	//    is an optional capability discovered by assertion. It must be the SAME key
	//    forwarded in the plan request below.
	ephPub := eph.PublicKey()
	if binder, ok := rt.(interface{ BindPublicKey([]byte) }); ok {
		binder.BindPublicKey(ephPub[:])
	}

	// Attest: boot the runtime, produce measurement + nonce (+ bound pubkey).
	ev, err := rt.Attest(ctx)
	if err != nil {
		res.FinalPhase = runtime.PhaseFailed
		return res, fmt.Errorf("migration: attestation: %w", err)
	}
	res.AttestationOK = len(ev.Measurement) > 0

	// 4. Request the sealed opaque plan from the backend. The provider forwards the
	//    ephemeral public key + attestation; it receives ciphertext only.
	in.DryRun = opt.DryRun
	pr, err := e.client.RequestPlan(ctx, in, ephPub, ev)
	if err != nil {
		res.FinalPhase = runtime.PhaseFailed
		return res, err
	}
	res.RunID = pr.RunID

	sealed, err := SealedInputsFrom(pr, opt.DryRun)
	if err != nil {
		res.FinalPhase = runtime.PhaseFailed
		return res, fmt.Errorf("migration: %w", err)
	}

	// 5. Hand the bundle to the runtime and stream coarse status. The private key
	//    is used ONLY inside the runtime's sealed boundary.
	stream, err := rt.Run(ctx, sealed, eph.private())
	if err != nil {
		res.FinalPhase = runtime.PhaseFailed
		return res, fmt.Errorf("migration: run: %w", err)
	}

	for st := range stream {
		res.Observations = append(res.Observations, st)
		res.FinalPhase = st.Phase
		res.Percent = st.Percent
		if st.Verdict != "" {
			res.Verdict = st.Verdict
		}
		if st.RolledBack {
			res.RolledBack = true
		}
	}
	return res, nil
}
