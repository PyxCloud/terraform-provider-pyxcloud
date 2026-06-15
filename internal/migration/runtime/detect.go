package runtime

// detect.go implements the `auto` substrate selection (MIGRATION.md §2.3, §7):
// pick the STRONGEST available substrate, with the sealed-WASM sandbox as the
// portable floor (never plaintext to the host).
//
//	confidential-container (if launchable) → hardware-TEE (SEV-SNP/TDX/SGX present)
//	→ sealed-WASM (always available)
//
// Both named substrates remain explicitly selectable via Select.

import "fmt"

// Select returns the ConfidentialRuntime for the requested substrate. For
// SubstrateAuto it detects and returns the strongest available. open is the
// HPKE opener the parent migration package injects; ccBackend selects the
// confidential-container backend (empty = nominal Nitro).
func Select(substrate Substrate, open Opener, ccBackend CCBackend) (ConfidentialRuntime, Capability, error) {
	switch substrate {
	case SubstrateAuto, "":
		return autoDetect(open, ccBackend)
	case SubstrateConfidentialContainer:
		rt := NewConfidentialContainer(open, ccBackend)
		return rt, rt.Detect(), nil
	case SubstrateLocalTEE:
		// "local-tee" = hardware TEE if present, else the sealed-WASM fallback.
		tee := NewHardwareTEE(open)
		if cap := tee.Detect(); cap.Available {
			return tee, cap, nil
		}
		sw := NewSealedWASM(open)
		return sw, sw.Detect(), nil
	case SubstrateSealedWASM:
		sw := NewSealedWASM(open)
		return sw, sw.Detect(), nil
	case SubstrateHardwareTEE:
		tee := NewHardwareTEE(open)
		return tee, tee.Detect(), nil
	default:
		return nil, Capability{}, fmt.Errorf("unknown confidential_runtime %q (want auto|local-tee|confidential-container)", substrate)
	}
}

// autoDetect probes substrates strongest-first and returns the first available,
// guaranteeing a sealed-WASM floor. It NEVER returns a plaintext/host-readable
// option.
func autoDetect(open Opener, ccBackend CCBackend) (ConfidentialRuntime, Capability, error) {
	// Candidate order = strongest → weakest.
	cc := NewConfidentialContainer(open, ccBackend)
	if cap := cc.Detect(); cap.Available {
		return cc, cap, nil
	}
	tee := NewHardwareTEE(open)
	if cap := tee.Detect(); cap.Available {
		return tee, cap, nil
	}
	sw := NewSealedWASM(open)
	cap := sw.Detect() // always available
	return sw, cap, nil
}

// DetectAll returns the capability report for every substrate on this host —
// useful for diagnostics and surfaced into the resource state so an operator can
// see what opacity tier was chosen and why.
func DetectAll(open Opener, ccBackend CCBackend) []Capability {
	return []Capability{
		NewConfidentialContainer(open, ccBackend).Detect(),
		NewHardwareTEE(open).Detect(),
		NewSealedWASM(open).Detect(),
	}
}
