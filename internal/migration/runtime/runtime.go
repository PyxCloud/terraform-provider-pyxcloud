// Package runtime defines the confidential-runtime abstraction the PyxCloud
// migration engine launches but cannot see into (MIGRATION.md §2.3).
//
// A ConfidentialRuntime is the sealed substrate that decrypts and executes the
// backend's opaque execution bundle. The provider/runner orchestrate its
// lifecycle but never observe the migration logic: they hand it sealed ciphertext
// and observe only coarse status. THIS PACKAGE CONTAINS ZERO MIGRATION LOGIC — it
// is a generic sealed sandbox + a generic opaque-step interpreter. The steps
// themselves come from the bundle, which is the backend's industrial secret.
//
// Three substrates implement the interface:
//   - sealed-WASM  (sealedwasm.go) — the portable, FUNCTIONAL fallback (the floor).
//   - hardware-TEE (hwtee.go)      — SEV-SNP/TDX/SGX; attestation STUBBED.
//   - confidential-container (cc.go) — Nitro/GCP CS/Azure CC; attestation STUBBED.
//
// The `auto` detector (detect.go) picks the strongest available, never plaintext.
package runtime

import "context"

// Substrate names the confidential-computing substrate. These mirror the
// migration{} schema's confidential_runtime values plus the internal substrates
// auto resolves to.
type Substrate string

const (
	// SubstrateAuto asks the engine to detect the strongest available substrate.
	SubstrateAuto Substrate = "auto"
	// SubstrateLocalTEE is the directive's "local-tee": a hardware TEE if present,
	// else the sealed-WASM memory-sealed sandbox fallback.
	SubstrateLocalTEE Substrate = "local-tee"
	// SubstrateConfidentialContainer is the directive's "confidential-container":
	// a remote-attested enclave container (Nitro / GCP Confidential Space / Azure CC).
	SubstrateConfidentialContainer Substrate = "confidential-container"

	// The following are concrete substrates auto can resolve to internally.

	// SubstrateSealedWASM is the portable memory-sealed WASM sandbox (the floor).
	SubstrateSealedWASM Substrate = "sealed-wasm"
	// SubstrateHardwareTEE is a hardware trusted execution environment.
	SubstrateHardwareTEE Substrate = "hardware-tee"
)

// Strength orders substrates from weakest (but never plaintext) to strongest, so
// the auto detector can pick the best the host offers.
type Strength int

const (
	// StrengthSealedWASM is the portable floor: SW memory-sealed sandbox.
	StrengthSealedWASM Strength = iota + 1
	// StrengthHardwareTEE is a hardware-isolated enclave on this host.
	StrengthHardwareTEE
	// StrengthConfidentialContainer is a remote-attested confidential container.
	StrengthConfidentialContainer
)

// Capability describes what a substrate can do on the current host.
type Capability struct {
	Substrate Substrate
	Strength  Strength
	// Available is true when this substrate can actually run here.
	Available bool
	// HardwareBacked is true for hardware-isolated enclaves (TEE / CC), false for
	// the SW sealed-WASM fallback.
	HardwareBacked bool
	// Detail is a human-readable note about detection (what was/wasn't found).
	Detail string
}

// Evidence is the attestation evidence a runtime produces at boot (§3.2): an
// enclave measurement plus a freshness nonce, forwarded with the plan request so
// the backend can decide whether to release the bundle's key (§3.3).
//
// The bytes are opaque to the provider/runner: they forward them, they do not
// interpret them.
type Evidence struct {
	// Substrate that produced this evidence.
	Substrate Substrate
	// Measurement is the enclave/runtime-image measurement (e.g. a PCR/MRENCLAVE/
	// launch-measurement digest). The backend compares it to an expected signed
	// value before releasing the key.
	Measurement []byte
	// Nonce is the per-run freshness challenge (anti-replay).
	Nonce []byte
	// Format identifies the attestation document type so the backend verifier can
	// parse it (e.g. "nitro-attestation-doc", "gcp-confidential-space-token",
	// "sev-snp-report", "sealed-wasm-local"). Opaque to the provider.
	Format string
	// Document is the raw, signed attestation document bytes (stubbed for the
	// cloud backends — see TODO(attestation-backend) markers in each runtime).
	Document []byte
}

// Phase is the coarse migration phase the runner is allowed to observe (§5). It
// deliberately does NOT name any migration step or strategy.
type Phase string

const (
	PhasePending    Phase = "pending"
	PhaseAttesting  Phase = "attesting"
	PhaseSyncing    Phase = "syncing"
	PhaseVerifying  Phase = "verifying"
	PhaseCutover    Phase = "cutover"
	PhaseSucceeded  Phase = "succeeded"
	PhaseRolledBack Phase = "rolled-back"
	PhaseFailed     Phase = "failed"
)

// Status is one coarse progress update. It carries phase + percentage + an
// optional verdict — never the method, never the step ordering (§5).
type Status struct {
	Phase      Phase
	Percent    int
	Verdict    string // set on terminal phases: "success" | "rolled-back" | "failed"
	Detail     string // coarse, opacity-safe note (e.g. "encrypted bytes transferred")
	RolledBack bool
}

// Terminal reports whether this status is a final one.
func (s Status) Terminal() bool {
	return s.Phase == PhaseSucceeded || s.Phase == PhaseRolledBack || s.Phase == PhaseFailed
}

// StatusStream is a read-only channel of coarse status updates the runner polls.
type StatusStream <-chan Status

// ConfidentialRuntime is the sealed substrate the engine launches but cannot see
// into. Implementations decrypt the bundle ONLY inside their sealed boundary and
// stream coarse status out.
type ConfidentialRuntime interface {
	// Detect reports whether this substrate can run on the current host and how
	// strong it is.
	Detect() Capability

	// Attest boots the runtime and produces attestation evidence (measurement +
	// nonce) to forward with the plan request (§3.2).
	Attest(ctx context.Context) (Evidence, error)

	// Run hands the sealed opaque bundle + sealed scoped creds into the sealed
	// boundary, decrypts them ONLY inside it, executes the opaque step program,
	// and streams coarse status. recipientPriv is the ephemeral private key; it is
	// used only inside the boundary and the caller must zeroize it after the run.
	// Plaintext (bundle, creds, key) never crosses back out.
	Run(ctx context.Context, sealed SealedInputs, recipientPriv [32]byte) (StatusStream, error)
}

// SealedInputs are the ciphertext inputs handed to a runtime: the opaque bundle
// and the scoped cloud credentials, both sealed to the ephemeral public key by
// the backend (§2.2). The provider/runner only ever hold these as ciphertext.
type SealedInputs struct {
	// Bundle is the sealed opaque execution bundle (the step program). Treated as
	// ciphertext bytes; never parsed on the provider/runner side.
	Bundle Sealed
	// Creds is the sealed scoped source+target cloud credentials for the data move.
	Creds Sealed
	// ExpectedMeasurement is the runtime-image measurement the bundle was sealed
	// to. Decryption is bound to it via AEAD AAD, so a forged measurement fails to
	// open (§3.3). Opaque to the provider beyond "this is what we attested as".
	ExpectedMeasurement []byte
	// DryRun, when set, asks the runtime to plan/verify without performing the
	// cutover.
	DryRun bool
}

// Sealed mirrors the engine's sealed-payload shape (an X25519 KEM public key +
// AEAD ciphertext) without importing the parent package, keeping this package's
// dependency surface clean. The parent migration package adapts between the two.
type Sealed struct {
	KEMPub     [32]byte
	Ciphertext []byte
}

// Opener opens a Sealed payload inside a runtime's sealed boundary. It is supplied
// by the parent migration package (which owns the crypto) so this package carries
// no key material handling of its own beyond what runs inside the seal. Returning
// an error means authentication failed (forged measurement / wrong key) and NO
// plaintext or key was released.
type Opener func(recipientPriv [32]byte, s Sealed, aad []byte) ([]byte, error)
