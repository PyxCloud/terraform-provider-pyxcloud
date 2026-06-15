package runtime

// interpreter.go is the GENERIC opaque-step interpreter that runs inside a sealed
// runtime boundary. It is the part that "runs whatever opaque steps the bundle
// carries" — it does NOT itself encode any migration logic (no CRIU, rsync, DB,
// secret, or queue sequencing). All of that lives in the bundle, which is the
// backend's industrial secret. This interpreter only knows how to:
//
//   - walk an ordered list of opaque steps it was handed,
//   - emit a coarse phase label per step (the runner's only visibility),
//   - honor a dry-run flag (verify, don't cut over),
//   - stop and report a rollback verdict if a step reports failure.
//
// The step opcodes here are intentionally generic transport/lifecycle markers
// (a phase tag + an opaque payload the interpreter never inspects). The bundle
// decoder (decodeBundle) is a thin, deliberately dumb deserializer: it turns the
// decrypted-inside-the-seal bytes into this generic step list WITHOUT attaching
// any semantics. If a future change started teaching this file what a step *does*,
// that would be migration logic and MUST move to the backend.

import (
	"context"
	"encoding/json"
	"fmt"
)

// opaqueStep is one generic step the interpreter executes. Phase is the coarse
// label surfaced to the runner; Payload is opaque bytes the interpreter passes
// through to the (host-sealed) executor without interpreting them.
type opaqueStep struct {
	// Phase is the coarse phase tag the runner is allowed to see (§5). It is just
	// a label — it does not tell the interpreter what to do.
	Phase Phase `json:"phase"`
	// Weight is a relative progress weight for percentage reporting.
	Weight int `json:"weight"`
	// Cutover marks a step that mutates the target/cuts over; skipped under dry-run.
	Cutover bool `json:"cutover"`
	// Payload is the opaque step body. The interpreter NEVER inspects it; in a real
	// enclave it would be fed to the sealed executor. Here it is carried opaquely.
	Payload json.RawMessage `json:"payload"`
}

// opaqueProgram is the decoded-inside-the-seal step program: an ordered list of
// opaque steps. This is the only structure the interpreter understands, and it
// is generic — the meaning of each step is sealed in Payload, never decoded here.
type opaqueProgram struct {
	// Steps is the ordered opaque step list.
	Steps []opaqueStep `json:"steps"`
}

// decodeBundle deserializes plaintext bundle bytes (already decrypted INSIDE the
// seal) into the generic opaque program. It attaches NO semantics: it is a dumb
// JSON decode into the generic step shape. A malformed bundle is an error.
//
// Note: in production the bundle is a compiled/serialized step program, not JSON;
// the format is the backend's choice. JSON here is just the testable stand-in for
// "opaque serialized steps" and changing it does not change the opacity property.
func decodeBundle(plaintext []byte) (opaqueProgram, error) {
	var p opaqueProgram
	if err := json.Unmarshal(plaintext, &p); err != nil {
		return opaqueProgram{}, fmt.Errorf("opaque program decode failed: %w", err)
	}
	if len(p.Steps) == 0 {
		return opaqueProgram{}, fmt.Errorf("opaque program has no steps")
	}
	return p, nil
}

// runProgram executes the opaque program step by step inside the seal, streaming
// coarse status. It emits the step's coarse phase + a running percentage, honors
// dryRun (cutover steps are verified, not applied), and reports a rollback verdict
// if a step is flagged failing (the bundle carries an opaque "fail" marker the
// generic interpreter recognizes only as "this step did not converge").
//
// The opaque payloads are never inspected; this loop is pure orchestration of
// generic steps. The returned channel is closed after the terminal status.
func runProgram(ctx context.Context, prog opaqueProgram, dryRun bool, out chan<- Status) {
	total := 0
	for _, s := range prog.Steps {
		w := s.Weight
		if w <= 0 {
			w = 1
		}
		total += w
	}
	done := 0
	emit := func(st Status) bool {
		select {
		case <-ctx.Done():
			return false
		case out <- st:
			return true
		}
	}

	emit(Status{Phase: PhasePending, Percent: 0, Detail: "sealed runtime ready"})

	for _, s := range prog.Steps {
		w := s.Weight
		if w <= 0 {
			w = 1
		}
		// A cutover step under dry-run is verified, not applied — coarse only.
		phase := s.Phase
		if s.Cutover && dryRun {
			phase = PhaseVerifying
		}
		// The generic interpreter recognizes one opaque control marker: a step
		// whose payload decodes to {"__converge__": false} means "did not
		// converge" → rollback. It does NOT understand WHAT failed to converge.
		if convergeFailed(s.Payload) {
			done += w
			emit(Status{
				Phase:      PhaseRolledBack,
				Percent:    pct(done, total),
				Verdict:    "rolled-back",
				RolledBack: true,
				Detail:     "a sealed step did not converge; source preserved",
			})
			return
		}
		done += w
		if !emit(Status{Phase: phase, Percent: pct(done, total), Detail: "encrypted bytes processed"}) {
			return
		}
	}

	if dryRun {
		emit(Status{Phase: PhaseSucceeded, Percent: 100, Verdict: "success", Detail: "dry-run verified; no cutover performed"})
		return
	}
	emit(Status{Phase: PhaseSucceeded, Percent: 100, Verdict: "success", Detail: "migration converged; cutover complete"})
}

// convergeFailed recognizes the single generic non-convergence control marker.
// It is intentionally the ONLY thing the interpreter understands about a payload;
// everything else is opaque.
func convergeFailed(payload json.RawMessage) bool {
	if len(payload) == 0 {
		return false
	}
	var ctrl struct {
		Converge *bool `json:"__converge__"`
	}
	if err := json.Unmarshal(payload, &ctrl); err != nil {
		return false // not a control payload; opaque, treated as normal
	}
	return ctrl.Converge != nil && !*ctrl.Converge
}

func pct(done, total int) int {
	if total <= 0 {
		return 100
	}
	p := done * 100 / total
	if p > 100 {
		p = 100
	}
	return p
}
