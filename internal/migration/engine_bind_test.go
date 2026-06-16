package migration

// engine_bind_test.go proves the engine binds the run's ephemeral public key into
// the confidential runtime BEFORE attesting. On the real Nitro path that key is
// embedded in the signed attestation document's public_key field; if the engine
// attested first (or never bound), the document would bind a nil key and the
// backend's AttestationVerifier pubkey-binding check (pyx-backend #176) would
// reject the run. This regression-guards engine.go step 3.

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/migration/runtime"
)

// bindingRuntime is a fake ConfidentialRuntime standing in for the Nitro
// confidential-container path. It records the key bound via BindPublicKey and
// whether the bind happened before Attest, and echoes the bound key out of Attest
// so the test can compare it to what the backend received.
type bindingRuntime struct {
	boundKey          []byte
	boundBeforeAttest bool
	attested          bool
}

func (r *bindingRuntime) Detect() runtime.Capability {
	return runtime.Capability{
		Substrate:      runtime.SubstrateConfidentialContainer,
		Available:      true,
		HardwareBacked: true,
		Detail:         "fake nitro binding runtime",
	}
}

// BindPublicKey records the run's ephemeral public key (mirrors
// ConfidentialContainer.BindPublicKey, which the engine discovers by assertion).
func (r *bindingRuntime) BindPublicKey(pub []byte) {
	r.boundKey = append([]byte(nil), pub...)
}

func (r *bindingRuntime) Attest(_ context.Context) (runtime.Evidence, error) {
	r.attested = true
	r.boundBeforeAttest = len(r.boundKey) > 0
	return runtime.Evidence{
		Substrate:   runtime.SubstrateConfidentialContainer,
		Measurement: []byte("test-measurement-pcr0"),
		Nonce:       []byte("test-nonce"),
		Format:      "nitro-attestation-doc",
		// A real Nitro doc binds the pubkey internally; expose it here so the test
		// can assert the engine bound the same key it forwards to the backend.
		Document: r.boundKey,
	}, nil
}

func (r *bindingRuntime) Run(_ context.Context, _ runtime.SealedInputs, _ [32]byte) (runtime.StatusStream, error) {
	ch := make(chan runtime.Status, 1)
	ch <- runtime.Status{Phase: runtime.PhaseSucceeded, Percent: 100, Verdict: "success"}
	close(ch)
	return ch, nil
}

// TestEngineBindsEphemeralPubKeyBeforeAttest asserts the engine calls
// BindPublicKey(eph.PublicKey()) before Attest, with the SAME key it sends to the
// backend in the plan request.
func TestEngineBindsEphemeralPubKeyBeforeAttest(t *testing.T) {
	t.Parallel()
	var gotPub [32]byte
	var gotAtt AttestationWire
	srv := fakeBackend(t, &gotPub, &gotAtt)
	defer srv.Close()

	fake := &bindingRuntime{}
	eng := NewEngine(NewClient(Config{Endpoint: srv.URL, Token: "test-token", HTTPClient: srv.Client()}))
	eng.selectRuntime = func(_ runtime.Substrate, _ runtime.Opener, _ runtime.CCBackend) (runtime.ConfidentialRuntime, runtime.Capability, error) {
		return fake, fake.Detect(), nil
	}

	if _, err := eng.Run(context.Background(), PlanInput{
		Place: "checkout", SourceProvider: "aws", TargetProvider: "gcp",
		SourceTopology: json.RawMessage(`{"name":"checkout"}`),
	}, Options{ConfidentialRuntime: runtime.SubstrateConfidentialContainer, CCBackend: runtime.CCBackendNitro}); err != nil {
		t.Fatalf("engine run: %v", err)
	}

	if !fake.attested {
		t.Fatal("runtime was never attested")
	}
	if !fake.boundBeforeAttest {
		t.Fatal("engine did NOT bind the ephemeral public key before Attest — the Nitro doc would bind a nil key and the BE verifier would reject the run")
	}
	if len(fake.boundKey) != 32 {
		t.Fatalf("bound key length = %d, want 32 (X25519)", len(fake.boundKey))
	}
	// The key bound into the attestation must be the SAME key forwarded to the
	// backend in the plan request, so the BE verifier's pubkey-binding passes.
	if gotPub == ([32]byte{}) {
		t.Fatal("backend never received an ephemeral public key")
	}
	if !bytes.Equal(fake.boundKey, gotPub[:]) {
		t.Errorf("bound key != ephemeral pubkey sent to backend:\n bound = %x\n sent  = %x", fake.boundKey, gotPub[:])
	}
}
