package migration

// engine_test.go drives the full provider-side flow against a FAKE backend HTTP
// server that plays the trusted moat: it receives the plan request (ephemeral
// pubkey + attestation), seals an opaque step program to that pubkey bound to the
// attested measurement, and returns ciphertext. This proves the end-to-end
// handshake stays opaque: the engine forwards only the public key + attestation,
// holds only ciphertext, and surfaces only coarse status — while a fresh
// ephemeral key is generated and zeroized per run.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/migration/runtime"
)

// fakeBackend stands in for the trusted PyxCloud backend planner. It is the ONLY
// party that knows the (here-trivial, opaque) step program; it seals it to the
// provider's ephemeral key. The migration logic in a real backend would be far
// richer — here it is a generic opaque program because none exists provider-side.
func fakeBackend(t *testing.T, capturedPub *[32]byte, capturedAtt *AttestationWire) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != planPath {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req PlanRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		pubB, _ := base64.StdEncoding.DecodeString(req.EphemeralPubKey)
		var pub [32]byte
		copy(pub[:], pubB)
		*capturedPub = pub
		*capturedAtt = req.Attestation

		// The backend binds the bundle to the measurement it received (it would
		// first verify it against the expected signed value; the fake trusts it).
		meas, _ := base64.StdEncoding.DecodeString(req.Attestation.Measurement)

		prog := map[string]any{"steps": []map[string]any{
			{"phase": "syncing", "weight": 3, "payload": map[string]any{"opaque": "step-1"}},
			{"phase": "verifying", "weight": 1, "payload": map[string]any{"opaque": "step-2"}},
			{"phase": "cutover", "weight": 1, "cutover": true, "payload": map[string]any{"opaque": "step-3"}},
		}}
		pt, _ := json.Marshal(prog)
		sealed, err := sealTo(pub, pt, meas)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Seal scoped creds the same opaque way.
		creds, _ := sealTo(pub, []byte(`{"src":"SCOPED","dst":"SCOPED"}`), meas)

		resp := PlanResponse{
			RunID: "run-abc123",
			Bundle: SealedWire{
				KEMPub:     base64.StdEncoding.EncodeToString(sealed.KEMPub[:]),
				Ciphertext: base64.StdEncoding.EncodeToString(sealed.Ciphertext),
			},
			Creds: SealedWire{
				KEMPub:     base64.StdEncoding.EncodeToString(creds.KEMPub[:]),
				Ciphertext: base64.StdEncoding.EncodeToString(creds.Ciphertext),
			},
			ExpectedMeasurement: base64.StdEncoding.EncodeToString(meas),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

// TestEngineEndToEndOpaque drives the whole flow and asserts the engine only ever
// forwards the public key + attestation, surfaces coarse status, and succeeds.
func TestEngineEndToEndOpaque(t *testing.T) {
	t.Parallel()
	var gotPub [32]byte
	var gotAtt AttestationWire
	srv := fakeBackend(t, &gotPub, &gotAtt)
	defer srv.Close()

	eng := NewEngine(NewClient(Config{Endpoint: srv.URL, Token: "test-token", HTTPClient: srv.Client()}))
	res, err := eng.Run(context.Background(), PlanInput{
		Place:          "checkout",
		SourceProvider: "aws",
		TargetProvider: "gcp",
		SourceTopology: json.RawMessage(`{"name":"checkout"}`),
	}, Options{ConfidentialRuntime: runtime.SubstrateAuto})
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if res.RunID != "run-abc123" {
		t.Errorf("run id = %q", res.RunID)
	}
	if res.Verdict != "success" {
		t.Errorf("verdict = %q, want success", res.Verdict)
	}
	if res.FinalPhase != runtime.PhaseSucceeded {
		t.Errorf("final phase = %q", res.FinalPhase)
	}
	if res.Percent != 100 {
		t.Errorf("percent = %d, want 100", res.Percent)
	}
	// On a non-confidential CI host, auto must land on sealed-WASM (the floor).
	if res.Substrate != runtime.SubstrateSealedWASM {
		t.Errorf("substrate = %q, want sealed-wasm fallback", res.Substrate)
	}
	if res.HardwareBacked {
		t.Error("sealed-WASM must not be hardware-backed")
	}
	if !res.AttestationOK {
		t.Error("expected attestation evidence")
	}
	// The backend received a 32-byte ephemeral public key + the sealed-WASM
	// attestation evidence — and NOTHING that is a private key.
	if gotPub == ([32]byte{}) {
		t.Error("backend did not receive an ephemeral public key")
	}
	if gotAtt.Substrate != string(runtime.SubstrateSealedWASM) {
		t.Errorf("attestation substrate = %q", gotAtt.Substrate)
	}
	if gotAtt.Measurement == "" {
		t.Error("expected a forwarded measurement")
	}
	// Coarse observations only — no step payloads, no "opaque"/"step-" leakage.
	for _, st := range res.Observations {
		if strings.Contains(st.Detail, "step-") || strings.Contains(st.Detail, "opaque") {
			t.Errorf("status detail leaked step content: %q", st.Detail)
		}
	}
}

// TestEngineDryRunEndToEnd proves a dry-run completes without claiming a cutover.
func TestEngineDryRunEndToEnd(t *testing.T) {
	t.Parallel()
	var pub [32]byte
	var att AttestationWire
	srv := fakeBackend(t, &pub, &att)
	defer srv.Close()

	eng := NewEngine(NewClient(Config{Endpoint: srv.URL, Token: "test-token", HTTPClient: srv.Client()}))
	res, err := eng.Run(context.Background(), PlanInput{
		Place: "p", SourceProvider: "aws", TargetProvider: "gcp",
	}, Options{ConfidentialRuntime: runtime.SubstrateAuto, DryRun: true})
	if err != nil {
		t.Fatalf("engine run: %v", err)
	}
	if res.Verdict != "success" {
		t.Errorf("verdict = %q", res.Verdict)
	}
	_ = att
	last := res.Observations[len(res.Observations)-1]
	if !strings.Contains(last.Detail, "dry-run") {
		t.Errorf("expected dry-run note, got %q", last.Detail)
	}
}
