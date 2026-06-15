package runtime

import (
	"context"
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

// TestStubbedRuntimesProduceEvidenceButStubDocument proves the cloud backends are
// wired at the interface level (they produce evidence) but their attestation
// document is an explicit stub the backend would reject in production.
func TestStubbedRuntimesProduceEvidenceButStubDocument(t *testing.T) {
	t.Parallel()
	for _, rt := range []ConfidentialRuntime{
		NewHardwareTEE(noopOpener),
		NewConfidentialContainer(noopOpener, CCBackendNitro),
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
		// The cloud document is an explicit stub (not a real signed quote).
		if !contains(ev.Document, "STUB:") {
			t.Errorf("%s: expected STUB document marker, got %q", ev.Substrate, string(ev.Document))
		}
	}
}

func contains(b []byte, s string) bool {
	return len(b) >= len(s) && string(b[:len(s)]) == s
}
