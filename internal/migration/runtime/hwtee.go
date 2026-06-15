package runtime

// hwtee.go is the hardware-TEE runtime: AMD SEV-SNP / Intel TDX / SGX
// (MIGRATION.md §2.3). The ABSTRACTION is real — detection, the attestation-
// evidence interface, and the sealed execution harness — but the cloud/CPU-
// specific attestation report generation is STUBBED behind clear
// TODO(attestation-backend) markers. The backend is swappable: wire one path,
// stub the rest, document.
//
// Functional today: detection (capability probe) + the sealed execution harness
// (it reuses the same generic interpreter under the seal as sealed-WASM, so a
// host that reports a TEE still runs migrations opaquely).
// Stubbed today: producing a genuine SEV-SNP/TDX/SGX attestation report — that
// requires the platform attestation driver and is left as an explicit TODO.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// hwTEEImage is the pinned measurement of the TEE runtime image (production: the
// launch measurement the platform reports; here a stable stand-in the backend
// would pin).
var hwTEEImage = []byte("pyxcloud-hardware-tee-runtime/v1")

func hwTEEMeasurement() []byte {
	d := sha256.Sum256(hwTEEImage)
	return d[:]
}

// HardwareTEE runs the bundle inside a CPU trusted execution environment.
type HardwareTEE struct {
	open Opener
	// detector is overridable in tests; defaults to probing the host.
	detector func() (present bool, detail string)
}

var _ ConfidentialRuntime = (*HardwareTEE)(nil)

// NewHardwareTEE builds the hardware-TEE runtime with the injected opener.
func NewHardwareTEE(open Opener) *HardwareTEE {
	return &HardwareTEE{open: open, detector: detectHostTEE}
}

// detectHostTEE probes the host for a CPU TEE. This is the one nominal real
// detection path: it checks the well-known Linux device/sysfs markers for
// SEV-SNP / TDX / SGX. On non-Linux or absent hardware it reports "not present"
// so auto falls back. (Producing the attestation report from these is the stub.)
func detectHostTEE() (bool, string) {
	markers := []struct {
		path string
		name string
	}{
		{"/dev/sev-guest", "AMD SEV-SNP"},
		{"/dev/tdx_guest", "Intel TDX"},
		{"/dev/sgx_enclave", "Intel SGX"},
	}
	for _, m := range markers {
		if _, err := os.Stat(m.path); err == nil {
			return true, m.name + " device present"
		}
	}
	return false, "no SEV-SNP/TDX/SGX device found"
}

func (r *HardwareTEE) Detect() Capability {
	present, detail := false, "no TEE detector"
	if r.detector != nil {
		present, detail = r.detector()
	}
	return Capability{
		Substrate:      SubstrateHardwareTEE,
		Strength:       StrengthHardwareTEE,
		Available:      present,
		HardwareBacked: true,
		Detail:         detail,
	}
}

// Attest would produce a genuine hardware attestation report. The interface +
// evidence shape are real; the report generation is STUBBED.
func (r *HardwareTEE) Attest(_ context.Context) (Evidence, error) {
	nonce := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Evidence{}, fmt.Errorf("hw-tee attest: nonce: %w", err)
	}
	// TODO(attestation-backend): generate a real SEV-SNP / TDX / SGX attestation
	// report binding `nonce` into REPORTDATA via the platform driver
	// (/dev/sev-guest GET_REPORT ioctl, TDX TDREPORT, or SGX EREPORT/quote), and
	// set Document to the signed report for the backend verifier. Until then we
	// return the pinned measurement with a clearly-marked stub document so the
	// backend can refuse it in production (the measurement matches sealed-exec, but
	// the Document is not a real signed quote).
	return Evidence{
		Substrate:   SubstrateHardwareTEE,
		Measurement: hwTEEMeasurement(),
		Nonce:       nonce,
		Format:      "sev-snp-report", // nominal; TDX/SGX swap the format string
		Document:    []byte("STUB:hardware-tee-attestation-report:not-a-real-quote"),
	}, nil
}

// Run reuses the same sealed boundary + generic interpreter as sealed-WASM: the
// bundle is opened only inside the seal, executed opaquely, and zeroized. In a
// full implementation the seal would be the CPU enclave rather than an in-process
// boundary, but the opacity contract is identical.
func (r *HardwareTEE) Run(ctx context.Context, in SealedInputs, recipientPriv [32]byte) (StatusStream, error) {
	// TODO(attestation-backend): the key release here must be gated by the
	// platform's remote-attestation handshake with the backend; for now the
	// sealed-exec boundary provides the opacity guarantee.
	sw := &SealedWASM{open: r.open}
	return sw.Run(ctx, in, recipientPriv)
}
