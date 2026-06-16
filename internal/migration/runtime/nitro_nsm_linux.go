//go:build linux

package runtime

// nitro_nsm_linux.go is the REAL Nitro attestation-document generation path. It is
// only compiled on linux (the NSM /dev/nsm device is linux-only) and only RUNS when
// the device is actually present — so on a non-enclave linux host or in CI it
// degrades cleanly with a documented error rather than fabricating a document.

import (
	"errors"
	"fmt"
	"os"

	"github.com/hf/nsm"
	"github.com/hf/nsm/request"
)

// errNoNSM is the documented degradation error when /dev/nsm is absent (not running
// inside a Nitro enclave). It is NOT a silent fake: the caller surfaces it so auto
// detection falls back to a TEE / sealed-WASM rather than emitting fake evidence.
var errNoNSM = errors.New("nitro NSM device /dev/nsm not present (not running inside a Nitro enclave)")

// nitroNSMAvailable reports whether the NSM device is present (the capability gate).
func nitroNSMAvailable() bool {
	_, err := os.Stat(nitroNSMPath)
	return err == nil
}

// generateNitroAttestation obtains a GENUINE COSE_Sign1/CBOR attestation document
// from the NSM, binding the backend-supplied `nonce` and the run's ephemeral
// `pubKey` (so the BE verifier can bind the released key to this enclave + run and
// reject replays). Returns errNoNSM when the device is unavailable.
func generateNitroAttestation(nonce, pubKey []byte) (document []byte, err error) {
	if !nitroNSMAvailable() {
		return nil, errNoNSM
	}
	sess, err := nsm.OpenDefaultSession()
	if err != nil {
		return nil, fmt.Errorf("nitro: open NSM session: %w", err)
	}
	defer sess.Close()

	res, err := sess.Send(&request.Attestation{
		Nonce:     nonce,  // backend challenge -> document `nonce` field (anti-replay)
		PublicKey: pubKey, // run ephemeral pubkey -> document `public_key` field (key binding)
	})
	if err != nil {
		return nil, fmt.Errorf("nitro: NSM attestation request: %w", err)
	}
	if res.Error != "" {
		return nil, fmt.Errorf("nitro: NSM returned error %q", res.Error)
	}
	if res.Attestation == nil || len(res.Attestation.Document) == 0 {
		return nil, errors.New("nitro: NSM returned an empty attestation document")
	}
	return res.Attestation.Document, nil
}
