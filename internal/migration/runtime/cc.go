package runtime

// cc.go is the confidential-container runtime: AWS Nitro Enclaves / GCP
// Confidential Space / Azure Confidential Containers (MIGRATION.md §2.3, §7). The
// runner launches a remote-attested enclave container that attests to the
// backend; the backend releases the bundle key ONLY to an enclave whose
// measurement matches the expected signed value.
//
// The ABSTRACTION is real and swappable (one backend wired nominally, the rest
// STUBBED with TODO(attestation-backend)). Per §7 the first backend to wire is
// left to build-time; we wire AWS Nitro as the nominal path and stub GCP/Azure.
//
// Functional today: backend selection + launchability detection + the sealed
// execution harness (generic interpreter under the seal).
// Stubbed today: launching the actual enclave + producing a genuine
// Nitro/GCP/Azure attestation document.

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
	// launchable is overridable in tests; defaults to probing the host.
	launchable func() (bool, string)
}

var _ ConfidentialRuntime = (*ConfidentialContainer)(nil)

// NewConfidentialContainer builds the CC runtime. backend selects which cloud
// backend to use; empty defaults to the nominal wired path (Nitro).
func NewConfidentialContainer(open Opener, backend CCBackend) *ConfidentialContainer {
	if backend == "" {
		backend = CCBackendNitro
	}
	return &ConfidentialContainer{open: open, backend: backend, launchable: detectCCLaunchable}
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

// Attest would launch the enclave and fetch its signed attestation document, then
// remote-attest to the backend. The interface + evidence shape are real; the
// document generation is STUBBED per backend.
func (r *ConfidentialContainer) Attest(_ context.Context) (Evidence, error) {
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Evidence{}, fmt.Errorf("cc attest: nonce: %w", err)
	}
	var format string
	switch r.backend {
	case CCBackendNitro:
		// TODO(attestation-backend): call the Nitro Security Module (NSM) to obtain
		// a real COSE_Sign1 attestation document with `nonce` in the user_data /
		// nonce field; set Document to the signed doc for the backend verifier.
		format = "nitro-attestation-doc"
	case CCBackendGCP:
		// TODO(attestation-backend): fetch a GCP Confidential Space attestation
		// token (OIDC) from the launcher metadata server and bind the nonce.
		format = "gcp-confidential-space-token"
	case CCBackendAzure:
		// TODO(attestation-backend): obtain an Azure CC / MAA attestation token.
		format = "azure-maa-token"
	default:
		return Evidence{}, fmt.Errorf("cc attest: unknown backend %q", r.backend)
	}
	return Evidence{
		Substrate:   SubstrateConfidentialContainer,
		Measurement: ccMeasurement(),
		Nonce:       nonce,
		Format:      format,
		Document:    []byte("STUB:" + string(r.backend) + ":not-a-real-attestation-document"),
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
