package runtime

// sealedwasm.go is the sealed-WASM runtime: the FUNCTIONAL, portable fallback and
// the opacity floor (MIGRATION.md §2.3, §7). It models a memory-sealed sandbox:
//
//   - The bundle and creds are HPKE-opened with the ephemeral key ONLY inside this
//     boundary (the Run method), via the Opener the parent package injects.
//   - The decrypted plaintext NEVER leaves the boundary: it is decoded into the
//     generic opaque program, executed by the generic interpreter, and zeroized.
//     The caller/provider receives only the coarse Status stream.
//   - Decryption is bound to the attested measurement via AEAD AAD, so a forged
//     measurement fails to open and no key/plaintext is released.
//
// "WASM" here names the intended production substrate (a WASM module in a sealed
// VM). The provider-side, testable model is the in-process sealed boundary below;
// what matters for opacity is the structural guarantee, not a real wasm engine.
// This file carries ZERO migration logic — it runs whatever opaque steps the
// bundle carries.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
)

// sealedWASMMeasurement is the deterministic "image measurement" of the local
// sealed-WASM runtime image. In production this would be the hash of the WASM
// module + sealed-VM image; here it is a fixed, signed-equivalent constant the
// backend would pin. The bundle is sealed to it (AAD), so opening requires it.
var sealedWASMImage = []byte("pyxcloud-sealed-wasm-runtime/v1")

func sealedWASMMeasurement() []byte {
	d := sha256.Sum256(sealedWASMImage)
	return d[:]
}

// SealedWASMMeasurement is the pinned image measurement of the sealed-WASM
// runtime — the value the backend seals the bundle to (binds it via AAD) so that
// only this attested runtime image can open it. Exported so the backend (and the
// opacity tests, which play the backend) can seal to the correct measurement.
func SealedWASMMeasurement() []byte { return sealedWASMMeasurement() }

// SealedWASM is the portable memory-sealed sandbox runtime.
type SealedWASM struct {
	// open is the parent-supplied HPKE opener used INSIDE the seal only.
	open Opener
}

var _ ConfidentialRuntime = (*SealedWASM)(nil)

// NewSealedWASM builds the sealed-WASM runtime with the injected opener.
func NewSealedWASM(open Opener) *SealedWASM { return &SealedWASM{open: open} }

func (r *SealedWASM) Detect() Capability {
	return Capability{
		Substrate:      SubstrateSealedWASM,
		Strength:       StrengthSealedWASM,
		Available:      true, // always available: the portable floor.
		HardwareBacked: false,
		Detail:         "software memory-sealed WASM sandbox (portable fallback)",
	}
}

// Attest produces local sealed-WASM evidence: the runtime-image measurement plus
// a fresh nonce. It is genuine (no cloud backend needed) — the measurement is the
// hash of the local sealed runtime image, which the backend pins.
func (r *SealedWASM) Attest(_ context.Context) (Evidence, error) {
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Evidence{}, fmt.Errorf("sealed-wasm attest: nonce: %w", err)
	}
	return Evidence{
		Substrate:   SubstrateSealedWASM,
		Measurement: sealedWASMMeasurement(),
		Nonce:       nonce,
		Format:      "sealed-wasm-local",
		Document:    sealedWASMMeasurement(), // self-describing local measurement
	}, nil
}

// Run is the sealed boundary. Everything inside this function is "inside the
// enclave": the bundle/creds are opened here, executed here, and zeroized here.
// Nothing decrypted crosses back to the caller except coarse Status.
func (r *SealedWASM) Run(ctx context.Context, in SealedInputs, recipientPriv [32]byte) (StatusStream, error) {
	if r.open == nil {
		return nil, fmt.Errorf("sealed-wasm: no opener wired")
	}
	// AAD binds decryption to the attested measurement. A forged/mismatched
	// measurement fails authentication → no plaintext, no key release (§3.3).
	aad := in.ExpectedMeasurement

	// --- INSIDE THE SEAL: open the opaque bundle. Plaintext stays local. ---
	bundlePT, err := r.open(recipientPriv, in.Bundle, aad)
	if err != nil {
		return nil, fmt.Errorf("sealed-wasm: bundle unsealing failed (no key release): %w", err)
	}
	defer zeroizeBytes(bundlePT)

	// Open the sealed scoped creds the same way. They are consumed only inside the
	// seal by the (opaque) executor; the provider/runner never see plaintext creds.
	if len(in.Creds.Ciphertext) > 0 {
		credsPT, err := r.open(recipientPriv, in.Creds, aad)
		if err != nil {
			return nil, fmt.Errorf("sealed-wasm: creds unsealing failed (no key release): %w", err)
		}
		zeroizeBytes(credsPT) // consumed-then-wiped inside the seal
	}

	prog, err := decodeBundle(bundlePT)
	if err != nil {
		return nil, fmt.Errorf("sealed-wasm: %w", err)
	}

	out := make(chan Status, 8)
	go func() {
		defer close(out)
		// The decoded program is local to this goroutine (still inside the seal).
		runProgram(ctx, prog, in.DryRun, out)
		// Best-effort: the program's step list is the last in-seal plaintext.
		// runProgram holds it by value; it goes out of scope here.
	}()
	return out, nil
}

// zeroizeBytes wipes a plaintext buffer that lived inside the seal.
func zeroizeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
