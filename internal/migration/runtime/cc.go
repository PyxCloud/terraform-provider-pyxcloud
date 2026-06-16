package runtime

// cc.go is the confidential-container runtime: AWS Nitro Enclaves / GCP
// Confidential Space / Azure Confidential Containers (MIGRATION.md §2.3, §7). The
// runner launches a remote-attested enclave container that attests to the
// backend; the backend releases the bundle key ONLY to an enclave whose
// measurement matches the expected signed value.
//
// PHASE 2: the AWS Nitro path now produces a GENUINE attestation document — a
// COSE_Sign1-wrapped, CBOR-encoded NSM document (PCRs + the enclave public key +
// the backend's nonce) via the /dev/nsm ioctl (see nitro.go / nitro_nsm_*.go). It
// is gated behind capability detection and degrades CLEANLY (documented error, not
// a silent fake) off a Nitro enclave. The GCP/Azure backends remain explicit STUBs.
//
// Functional today: backend selection + launchability detection + the sealed
// execution harness (generic interpreter under the seal); REAL Nitro attestation.
// Stubbed today: launching the actual enclave + producing a genuine GCP/Azure
// attestation document.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// CCBackend names a confidential-container backend.
type CCBackend string

const (
	CCBackendNitro CCBackend = "aws-nitro"              // nominal wired path
	CCBackendGCP   CCBackend = "gcp-confidential-space" // STUBBED
	CCBackendAzure CCBackend = "azure-cc"               // STUBBED
)

var ccImage = []byte("pyxcloud-confidential-container-runtime/v1")

func ccMeasurement() []byte {
	d := sha256.Sum256(ccImage)
	return d[:]
}

// ConfidentialContainer launches a remote-attested confidential container.
type ConfidentialContainer struct {
	open    Opener
	backend CCBackend
	// ephemeralPubKey, when set, is the run's ephemeral X25519 public key. The Nitro
	// path binds it into the attestation document's public_key field so the backend
	// can bind the released key to THIS run (§3 steps 2-4). Set via BindPublicKey.
	ephemeralPubKey []byte
	// launchable is overridable in tests; defaults to probing the host.
	launchable func() (bool, string)
	// attestNitro is overridable in tests; defaults to the real NSM generator. It
	// returns the COSE_Sign1 document binding nonce + pubKey, or a documented error
	// when NSM is unavailable.
	attestNitro func(nonce, pubKey []byte) ([]byte, error)
}

var _ ConfidentialRuntime = (*ConfidentialContainer)(nil)

// NewConfidentialContainer builds the CC runtime. backend selects which cloud
// backend to use; empty defaults to the nominal wired path (Nitro).
func NewConfidentialContainer(open Opener, backend CCBackend) *ConfidentialContainer {
	if backend == "" {
		backend = CCBackendNitro
	}
	return &ConfidentialContainer{
		open:        open,
		backend:     backend,
		launchable:  detectCCLaunchable,
		attestNitro: generateNitroAttestation,
	}
}

// BindPublicKey records the run's ephemeral public key so the Nitro attestation
// document binds it (public_key field). The engine calls this before Attest with
// the same key it sends in the plan request, so the BE verifier can confirm the
// released key is bound to this enclave + run.
func (r *ConfidentialContainer) BindPublicKey(pub []byte) {
	r.ephemeralPubKey = pub
}

// detectCCLaunchable probes whether a confidential container can be launched here.
// Nominal real check: the AWS Nitro Enclaves allocator device. Absent → not
// launchable, so auto falls back to a TEE or sealed-WASM.
func detectCCLaunchable() (bool, string) {
	if _, err := os.Stat("/dev/nitro_enclaves"); err == nil {
		return true, "AWS Nitro Enclaves allocator present"
	}
	// TODO(attestation-backend): add GCP Confidential Space (metadata-server
	// confidential-space probe) and Azure CC (CVM/SEV-SNP guest) launch detection.
	return false, "no confidential-container backend launchable (Nitro allocator absent; GCP/Azure detection TODO)"
}

func (r *ConfidentialContainer) Detect() Capability {
	launchable, detail := false, "no CC detector"
	if r.launchable != nil {
		launchable, detail = r.launchable()
	}
	return Capability{
		Substrate:      SubstrateConfidentialContainer,
		Strength:       StrengthConfidentialContainer,
		Available:      launchable,
		HardwareBacked: true,
		Detail:         fmt.Sprintf("%s [%s]", detail, r.backend),
	}
}

// Attest boots the runtime and produces attestation evidence to forward with the
// plan request (§3.2). The AWS Nitro path is REAL: it asks the NSM for a genuine
// COSE_Sign1/CBOR attestation document binding the fresh `nonce` and the run's
// ephemeral public key, then extracts PCR0 as the measurement the backend pins.
// GCP/Azure remain explicit STUBs. When the Nitro NSM device is unavailable the
// call degrades CLEANLY with a documented error (NOT a silent fake) so auto
// detection can fall back.
func (r *ConfidentialContainer) Attest(_ context.Context) (Evidence, error) {
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Evidence{}, fmt.Errorf("cc attest: nonce: %w", err)
	}
	switch r.backend {
	case CCBackendNitro:
		return r.attestNitroEvidence(nonce)
	case CCBackendGCP:
		// TODO(attestation-backend): fetch a GCP Confidential Space attestation
		// token (OIDC) from the launcher metadata server and bind the nonce.
		return Evidence{
			Substrate:   SubstrateConfidentialContainer,
			Measurement: ccMeasurement(),
			Nonce:       nonce,
			Format:      "gcp-confidential-space-token",
			Document:    []byte("STUB:" + string(r.backend) + ":not-a-real-attestation-document"),
		}, nil
	case CCBackendAzure:
		// TODO(attestation-backend): obtain an Azure CC / MAA attestation token.
		return Evidence{
			Substrate:   SubstrateConfidentialContainer,
			Measurement: ccMeasurement(),
			Nonce:       nonce,
			Format:      "azure-maa-token",
			Document:    []byte("STUB:" + string(r.backend) + ":not-a-real-attestation-document"),
		}, nil
	default:
		return Evidence{}, fmt.Errorf("cc attest: unknown backend %q", r.backend)
	}
}

// attestNitroEvidence produces REAL Nitro evidence: it obtains the genuine
// COSE_Sign1 attestation document from the NSM (binding nonce + ephemeral pubkey),
// parses it to lift PCR0 as the measurement, and returns the signed document for
// the backend verifier. On a non-enclave host the NSM call degrades cleanly and
// the error is surfaced (no fake document).
func (r *ConfidentialContainer) attestNitroEvidence(nonce []byte) (Evidence, error) {
	gen := r.attestNitro
	if gen == nil {
		gen = generateNitroAttestation
	}
	doc, err := gen(nonce, r.ephemeralPubKey)
	if err != nil {
		// Documented clean degradation — NOT a silent fake. auto falls back.
		return Evidence{}, fmt.Errorf("cc attest (nitro): %w", err)
	}
	// Lift PCR0 from the signed document so Evidence.Measurement is the genuine
	// enclave-image measurement the backend pins (rather than a synthetic constant).
	parsed, err := ParseNitroDocument(doc)
	if err != nil {
		return Evidence{}, fmt.Errorf("cc attest (nitro): NSM returned an unparseable document: %w", err)
	}
	measurement := parsed.Measurement()
	if len(measurement) == 0 {
		return Evidence{}, fmt.Errorf("cc attest (nitro): attestation document carries no PCR%d measurement", nitroMeasurementPCR)
	}
	return Evidence{
		Substrate:   SubstrateConfidentialContainer,
		Measurement: measurement,
		Nonce:       nonce,
		Format:      NitroFormat,
		Document:    doc, // genuine COSE_Sign1/CBOR document for the BE verifier
	}, nil
}

// Run reuses the sealed boundary + generic interpreter. In a full implementation
// the seal is the launched enclave and the key is released to it only after the
// backend verifies its remote attestation; here the sealed-exec boundary provides
// the opacity guarantee.
func (r *ConfidentialContainer) Run(ctx context.Context, in SealedInputs, recipientPriv [32]byte) (StatusStream, error) {
	// TODO(attestation-backend): launch the confidential container, complete the
	// remote-attestation handshake with the backend, and let the BE release the
	// CEK to the attested enclave. Until launch is wired, run the sealed-exec
	// harness so the path is functional and opaque.
	sw := &SealedWASM{open: r.open}
	return sw.Run(ctx, in, recipientPriv)
}
