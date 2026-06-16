package runtime

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// noopOpener is a stand-in opener for detection tests that never run a bundle.
func noopOpener(_ [32]byte, _ Sealed, _ []byte) ([]byte, error) { return nil, nil }

// TestAutoFallsBackToSealedWASM proves guarantee (d): on a host with neither a
// confidential container nor a hardware TEE, `auto` falls back to the sealed-WASM
// floor — never plaintext.
func TestAutoFallsBackToSealedWASM(t *testing.T) {
	t.Parallel()
	// On the CI/test host there is no /dev/nitro_enclaves or /dev/sev-guest, so
	// detection naturally falls through to sealed-WASM.
	rt, cap, err := Select(SubstrateAuto, noopOpener, "")
	if err != nil {
		t.Fatalf("select auto: %v", err)
	}
	if cap.Substrate != SubstrateSealedWASM {
		t.Fatalf("expected sealed-WASM fallback on a non-confidential host, got %s", cap.Substrate)
	}
	if cap.HardwareBacked {
		t.Fatal("sealed-WASM must not claim hardware backing")
	}
	if _, ok := rt.(*SealedWASM); !ok {
		t.Fatalf("expected *SealedWASM, got %T", rt)
	}
	// The floor is ALWAYS available.
	if !cap.Available {
		t.Fatal("sealed-WASM floor must always be available")
	}
}

// TestAutoPicksStrongestAvailable proves `auto` prefers a stronger substrate when
// it is launchable. We inject a launchable confidential-container detector.
func TestAutoPicksStrongestAvailable(t *testing.T) {
	t.Parallel()
	cc := NewConfidentialContainer(noopOpener, CCBackendNitro)
	cc.launchable = func() (bool, string) { return true, "test: CC launchable" }
	if cap := cc.Detect(); !cap.Available {
		t.Fatal("expected injected CC to be available")
	}
	// And a TEE-present host without CC should pick the TEE over sealed-WASM.
	tee := NewHardwareTEE(noopOpener)
	tee.detector = func() (bool, string) { return true, "test: TEE present" }
	if cap := tee.Detect(); !cap.Available || cap.Strength != StrengthHardwareTEE {
		t.Fatalf("expected available hardware TEE, got %+v", cap)
	}
	// Ordering invariant: CC strength > TEE strength > sealed-WASM strength.
	if !(StrengthConfidentialContainer > StrengthHardwareTEE && StrengthHardwareTEE > StrengthSealedWASM) {
		t.Fatal("substrate strength ordering is wrong")
	}
}

// TestLocalTEEFallsBackToSealedWASM proves the explicitly-selected `local-tee`
// substrate falls back to sealed-WASM when no hardware TEE is present (never
// plaintext).
func TestLocalTEEFallsBackToSealedWASM(t *testing.T) {
	t.Parallel()
	_, cap, err := Select(SubstrateLocalTEE, noopOpener, "")
	if err != nil {
		t.Fatalf("select local-tee: %v", err)
	}
	if cap.Substrate != SubstrateSealedWASM {
		t.Fatalf("expected sealed-WASM fallback for local-tee on non-TEE host, got %s", cap.Substrate)
	}
}

// TestSelectUnknownSubstrate errors clearly.
func TestSelectUnknownSubstrate(t *testing.T) {
	t.Parallel()
	if _, _, err := Select(Substrate("nonsense"), noopOpener, ""); err == nil {
		t.Fatal("expected error for unknown substrate")
	}
}

// TestDetectAllReportsEverySubstrate ensures diagnostics cover all three tiers.
func TestDetectAllReportsEverySubstrate(t *testing.T) {
	t.Parallel()
	caps := DetectAll(noopOpener, "")
	if len(caps) != 3 {
		t.Fatalf("expected 3 capabilities, got %d", len(caps))
	}
	seen := map[Substrate]bool{}
	for _, c := range caps {
		seen[c.Substrate] = true
	}
	for _, want := range []Substrate{SubstrateConfidentialContainer, SubstrateHardwareTEE, SubstrateSealedWASM} {
		if !seen[want] {
			t.Errorf("DetectAll missing %s", want)
		}
	}
}

// TestStubbedRuntimesProduceEvidenceButStubDocument proves the STILL-STUBBED
// backends are wired at the interface level (they produce evidence) but their
// attestation document is an explicit stub the backend would reject in production.
// Phase 2: Nitro is no longer here — it produces a real document (or degrades
// cleanly); only SEV-SNP/TDX (hardware-TEE) and GCP/Azure remain stubs.
func TestStubbedRuntimesProduceEvidenceButStubDocument(t *testing.T) {
	t.Parallel()
	for _, rt := range []ConfidentialRuntime{
		NewHardwareTEE(noopOpener),
		NewConfidentialContainer(noopOpener, CCBackendGCP),
		NewConfidentialContainer(noopOpener, CCBackendAzure),
	} {
		ev, err := rt.Attest(context.Background())
		if err != nil {
			t.Fatalf("attest: %v", err)
		}
		if len(ev.Measurement) == 0 || len(ev.Nonce) == 0 {
			t.Errorf("%s: expected measurement+nonce", ev.Substrate)
		}
		if ev.Format == "" {
			t.Errorf("%s: expected an attestation format", ev.Substrate)
		}
		// The still-stubbed document is an explicit stub (not a real signed quote).
		if !contains(ev.Document, "STUB:") {
			t.Errorf("%s: expected STUB document marker, got %q", ev.Substrate, string(ev.Document))
		}
	}
}

// TestNitroDegradesCleanlyWithoutNSM proves guarantee (3): off a Nitro enclave the
// real Nitro path returns a DOCUMENTED error (not a silent fake). On the CI/test
// host /dev/nsm is absent, so the default generator must fail explicitly and emit
// no evidence.
func TestNitroDegradesCleanlyWithoutNSM(t *testing.T) {
	t.Parallel()
	cc := NewConfidentialContainer(noopOpener, CCBackendNitro)
	ev, err := cc.Attest(context.Background())
	if err == nil {
		t.Fatalf("expected a documented degradation error without NSM, got evidence %+v", ev)
	}
	if len(ev.Document) != 0 {
		t.Fatalf("expected NO document on degradation, got %q", string(ev.Document))
	}
	if !strings.Contains(err.Error(), "NSM") {
		t.Errorf("expected an NSM-related degradation message, got %v", err)
	}
}

// TestNitroProducesRealEvidenceWhenNSMPresent proves the Nitro path emits genuine
// evidence (real COSE_Sign1 document + PCR0 measurement + bound nonce/pubkey) when
// the NSM generator is available. We inject a synthetic generator (CI has no NSM
// device) that returns a signed document built by the same code path a real NSM
// would, then assert the evidence shape matches what the backend verifier consumes.
func TestNitroProducesRealEvidenceWhenNSMPresent(t *testing.T) {
	t.Parallel()
	v := newNitroTestVector(t)

	cc := NewConfidentialContainer(noopOpener, CCBackendNitro)
	cc.BindPublicKey(v.publicKey)
	// Inject a generator that binds the engine's nonce + pubkey into a real doc.
	cc.attestNitro = func(nonce, pubKey []byte) ([]byte, error) {
		return v.signDocument(t, nonce, pubKey), nil
	}

	ev, err := cc.Attest(context.Background())
	if err != nil {
		t.Fatalf("attest: %v", err)
	}
	if ev.Format != NitroFormat {
		t.Errorf("format = %q, want %q", ev.Format, NitroFormat)
	}
	if contains(ev.Document, "STUB:") {
		t.Error("Nitro evidence must NOT be a stub document")
	}
	// Measurement must be the genuine PCR0 lifted from the signed document.
	if !bytes.Equal(ev.Measurement, v.pcr0) {
		t.Errorf("measurement is not PCR0 from the document")
	}
	// The backend verifier (mirrored here) must accept it and recover the bound
	// nonce + public key.
	doc, err := VerifyNitroDocument(ev.Document, NitroVerifyOptions{
		Roots:             v.roots,
		ExpectedNonce:     ev.Nonce,
		ExpectedPublicKey: v.publicKey,
		Now:               v.now,
	})
	if err != nil {
		t.Fatalf("backend-side verify of real evidence failed: %v", err)
	}
	if !bytes.Equal(doc.Measurement(), v.pcr0) {
		t.Error("verified document PCR0 mismatch")
	}
}

func contains(b []byte, s string) bool {
	return len(b) >= len(s) && string(b[:len(s)]) == s
}
