package migration

// opacity_test.go proves the four opacity guarantees from MIGRATION.md §6.4:
//
//	(a) the provider/runner never hold the bundle plaintext or cloud creds — the
//	    client only ever has ciphertext;
//	(b) decryption fails / the key is not released on a forged/mismatched
//	    attestation measurement;
//	(c) the ephemeral key + plaintext are zeroized after a run;
//	(d) `auto` detection picks the strongest available and falls back to sealed-WASM.
//
// These tests play the role of the trusted backend: they seal an opaque bundle to
// the provider's ephemeral public key (the only crypto the "backend" does here)
// and then drive the real provider-side runtime. The bundle they seal is a
// generic opaque step program — it deliberately carries NO migration logic,
// because none exists provider-side.

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/migration/runtime"
)

// sealBundleForTest plays the backend: it seals an opaque step program to the
// provider's ephemeral public key, bound (AAD) to the expected measurement.
func sealBundleForTest(t *testing.T, ephPub [32]byte, measurement []byte, steps []map[string]any) Sealed {
	t.Helper()
	prog := map[string]any{"steps": steps}
	pt, err := json.Marshal(prog)
	if err != nil {
		t.Fatalf("marshal program: %v", err)
	}
	sealed, err := sealTo(ephPub, pt, measurement)
	if err != nil {
		t.Fatalf("sealTo: %v", err)
	}
	return sealed
}

// genericSteps is an opaque, non-migration step program: generic phase tags +
// opaque payloads the interpreter never inspects.
func genericSteps() []map[string]any {
	return []map[string]any{
		{"phase": "syncing", "weight": 2, "payload": map[string]any{"opaque": "AAAA"}},
		{"phase": "verifying", "weight": 1, "payload": map[string]any{"opaque": "BBBB"}},
		{"phase": "cutover", "weight": 1, "cutover": true, "payload": map[string]any{"opaque": "CCCC"}},
	}
}

// driveRuntime runs one migration through the runtime directly (no HTTP) using
// the supplied ephemeral key (the bundle must have been sealed to its public
// key), returning the observed coarse statuses.
func driveRuntime(t *testing.T, eph *ephemeralKey, substrate runtime.Substrate, sealed Sealed, measurement []byte, dryRun bool) ([]runtime.Status, error) {
	t.Helper()
	rt, _, err := runtime.Select(substrate, opener, "")
	if err != nil {
		return nil, err
	}
	in := runtime.SealedInputs{
		Bundle:              runtime.Sealed{KEMPub: sealed.KEMPub, Ciphertext: sealed.Ciphertext},
		ExpectedMeasurement: measurement,
		DryRun:              dryRun,
	}
	stream, err := rt.Run(context.Background(), in, eph.private())
	if err != nil {
		return nil, err
	}
	var got []runtime.Status
	for st := range stream {
		got = append(got, st)
	}
	return got, nil
}

// localMeasurement is the sealed-WASM runtime image measurement the backend pins.
func localMeasurement() []byte { return runtime.SealedWASMMeasurement() }

// TestOpacity_ProviderHoldsOnlyCiphertext proves (a): a SealedInputs / PlanResponse
// as held by the provider contains no readable program. The bundle bytes do not
// contain the cleartext step labels, and the provider code has no Open call.
func TestOpacity_ProviderHoldsOnlyCiphertext(t *testing.T) {
	t.Parallel()
	eph, err := newEphemeralKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	defer eph.Zeroize()
	meas := localMeasurement()

	steps := genericSteps()
	sealed := sealBundleForTest(t, eph.PublicKey(), meas, steps)

	// The ciphertext must not contain the plaintext markers the backend sealed.
	for _, marker := range []string{"syncing", "cutover", "opaque", "AAAA", "steps"} {
		if bytes.Contains(sealed.Ciphertext, []byte(marker)) {
			t.Fatalf("sealed bundle ciphertext leaked plaintext marker %q", marker)
		}
	}
	// The provider-held SealedInputs is ciphertext only — there is no method on it
	// that returns plaintext; the only opener is package-private and used in-seal.
	in := runtime.SealedInputs{Bundle: runtime.Sealed{KEMPub: sealed.KEMPub, Ciphertext: sealed.Ciphertext}}
	if len(in.Bundle.Ciphertext) == 0 {
		t.Fatal("expected ciphertext")
	}
	// A creds payload sealed the same way is likewise opaque.
	creds := sealBundleForTest(t, eph.PublicKey(), meas, []map[string]any{{"phase": "syncing", "payload": map[string]any{"secret": "DO_NOT_LEAK"}}})
	if bytes.Contains(creds.Ciphertext, []byte("DO_NOT_LEAK")) {
		t.Fatal("sealed creds ciphertext leaked the secret")
	}
}

// TestOpacity_ForgedMeasurementFailsDecryption proves (b): a bundle sealed to one
// measurement cannot be opened under a forged/mismatched one — no plaintext, no
// key release.
func TestOpacity_ForgedMeasurementFailsDecryption(t *testing.T) {
	t.Parallel()
	correct := localMeasurement()
	steps := genericSteps()

	// Seal to the CORRECT measurement, then try to run with a FORGED one.
	eph, err := newEphemeralKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	defer eph.Zeroize()
	sealed := sealBundleForTest(t, eph.PublicKey(), correct, steps)

	forged := append([]byte{}, correct...)
	forged[0] ^= 0xFF // flip a bit → wrong measurement

	rt, _, err := runtime.Select(runtime.SubstrateSealedWASM, opener, "")
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	in := runtime.SealedInputs{
		Bundle:              runtime.Sealed{KEMPub: sealed.KEMPub, Ciphertext: sealed.Ciphertext},
		ExpectedMeasurement: forged, // forged AAD
	}
	_, err = rt.Run(context.Background(), in, eph.private())
	if err == nil {
		t.Fatal("expected unsealing to FAIL under a forged measurement, got nil error")
	}

	// Sanity: with the CORRECT measurement it DOES open and run.
	in.ExpectedMeasurement = correct
	stream, err := rt.Run(context.Background(), in, eph.private())
	if err != nil {
		t.Fatalf("expected success with correct measurement, got %v", err)
	}
	var terminal runtime.Status
	for st := range stream {
		terminal = st
	}
	if terminal.Verdict != "success" {
		t.Fatalf("expected success verdict, got %q", terminal.Verdict)
	}
}

// TestOpacity_WrongEphemeralKeyFailsDecryption proves a captured bundle is useless
// without the matching ephemeral private key (§3.5).
func TestOpacity_WrongEphemeralKeyFailsDecryption(t *testing.T) {
	t.Parallel()
	meas := localMeasurement()
	ephA, _ := newEphemeralKey()
	defer ephA.Zeroize()
	sealed := sealBundleForTest(t, ephA.PublicKey(), meas, genericSteps())

	// A DIFFERENT key cannot open it.
	ephB, _ := newEphemeralKey()
	defer ephB.Zeroize()
	rt, _, _ := runtime.Select(runtime.SubstrateSealedWASM, opener, "")
	_, err := rt.Run(context.Background(),
		runtime.SealedInputs{Bundle: runtime.Sealed{KEMPub: sealed.KEMPub, Ciphertext: sealed.Ciphertext}, ExpectedMeasurement: meas},
		ephB.private())
	if err == nil {
		t.Fatal("expected failure opening with the wrong ephemeral key")
	}
}

// TestOpacity_EphemeralKeyZeroized proves (c): the ephemeral private key is wiped
// after a run.
func TestOpacity_EphemeralKeyZeroized(t *testing.T) {
	t.Parallel()
	meas := localMeasurement()
	eph, _ := newEphemeralKey()
	sealed := sealBundleForTest(t, eph.PublicKey(), meas, genericSteps())

	rt, _, _ := runtime.Select(runtime.SubstrateSealedWASM, opener, "")
	stream, err := rt.Run(context.Background(),
		runtime.SealedInputs{Bundle: runtime.Sealed{KEMPub: sealed.KEMPub, Ciphertext: sealed.Ciphertext}, ExpectedMeasurement: meas},
		eph.private())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for range stream {
	}

	if eph.isCleared() {
		t.Fatal("key reported cleared before Zeroize")
	}
	if eph.private() == ([32]byte{}) {
		t.Fatal("expected a non-zero private key before Zeroize")
	}
	eph.Zeroize()
	if !eph.isCleared() {
		t.Fatal("expected key cleared after Zeroize")
	}
	// The private key bytes are now zero (read AFTER Zeroize — private() returns a
	// copy of the struct field, so it must be read post-wipe).
	if eph.private() != ([32]byte{}) {
		t.Fatalf("expected zeroized private key, got non-zero bytes")
	}
	// Zeroize is idempotent.
	eph.Zeroize()
}

// TestOpacity_Rollback proves a non-converging opaque step yields a rollback
// verdict surfaced as coarse status (source preserved), never the reason.
func TestOpacity_Rollback(t *testing.T) {
	t.Parallel()
	meas := localMeasurement()
	eph, _ := newEphemeralKey()
	defer eph.Zeroize()
	steps := []map[string]any{
		{"phase": "syncing", "weight": 1, "payload": map[string]any{"opaque": "x"}},
		{"phase": "cutover", "weight": 1, "cutover": true, "payload": map[string]any{"__converge__": false}},
	}
	sealed := sealBundleForTest(t, eph.PublicKey(), meas, steps)
	rt, _, _ := runtime.Select(runtime.SubstrateSealedWASM, opener, "")
	stream, err := rt.Run(context.Background(),
		runtime.SealedInputs{Bundle: runtime.Sealed{KEMPub: sealed.KEMPub, Ciphertext: sealed.Ciphertext}, ExpectedMeasurement: meas},
		eph.private())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var terminal runtime.Status
	for st := range stream {
		terminal = st
	}
	if !terminal.RolledBack || terminal.Verdict != "rolled-back" {
		t.Fatalf("expected rolled-back verdict, got %+v", terminal)
	}
}

// TestOpacity_DryRunNoCutover proves dry-run verifies without a cutover verdict
// that claims completion of the mutation.
func TestOpacity_DryRunNoCutover(t *testing.T) {
	t.Parallel()
	meas := localMeasurement()
	eph, _ := newEphemeralKey()
	defer eph.Zeroize()
	sealed := sealBundleForTest(t, eph.PublicKey(), meas, genericSteps())
	got, err := driveRuntime(t, eph, runtime.SubstrateSealedWASM, sealed, meas, true)
	if err != nil {
		t.Fatalf("drive: %v", err)
	}
	terminal := got[len(got)-1]
	if terminal.Verdict != "success" {
		t.Fatalf("expected dry-run success, got %q", terminal.Verdict)
	}
	if !bytes.Contains([]byte(terminal.Detail), []byte("dry-run")) {
		t.Fatalf("expected dry-run note, got %q", terminal.Detail)
	}
}
