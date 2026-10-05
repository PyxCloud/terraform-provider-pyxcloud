// Package billingscan applies costsignalrule rules to billing cost data and
// returns any triggered CostBlowoutSignals. It is the runtime layer that sits
// above the rule-type definitions in costsignalrule.
package billingscan

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/costsignalrule"
)

// Input is the billing cost data fed to the scanner.
type Input struct {
	// Current is a map of resource-type -> current cost (USD, monthly).
	Current map[string]float64 `json:"current"`
	// Baseline is a map of resource-type -> expected/previous cost (USD, monthly).
	// Used for CostSpike and ResourceCostAnomaly rules.
	Baseline map[string]float64 `json:"baseline"`
}

// Result is the output of a scanner run.
type Result struct {
	// Signals are the rules that fired.
	Signals []costsignalrule.CostBlowoutSignal `json:"signals"`
	// OK is true when no rules fired.
	OK bool `json:"ok"`
	// ScannedAt is the UTC time the scan was performed.
	ScannedAt time.Time `json:"scanned_at"`
}

// Scanner holds an ordered list of rules to evaluate.
type Scanner struct {
	Rules []costsignalrule.CostBlowoutRule
}

// Default returns a Scanner pre-loaded with all built-in rules.
func Default() *Scanner {
	return &Scanner{
		Rules: []costsignalrule.CostBlowoutRule{
			costsignalrule.DefaultBudgetOverrunRule,
			costsignalrule.DefaultCostSpikeRule,
			costsignalrule.DefaultResourceCostAnomalyRule,
		},
	}
}

// New returns a Scanner with the supplied rules.
func New(rules []costsignalrule.CostBlowoutRule) *Scanner {
	return &Scanner{Rules: rules}
}

// Scan evaluates all rules against the provided Input and returns a Result.
func (s *Scanner) Scan(in Input) Result {
	res := Result{
		ScannedAt: time.Now().UTC(),
	}
	for i := range s.Rules {
		sig := s.Rules[i].Evaluate(in.Current, in.Baseline)
		if sig != nil {
			res.Signals = append(res.Signals, *sig)
		}
	}
	res.OK = len(res.Signals) == 0
	return res
}

// ScanReader reads Input JSON from r, runs the default scanner, and returns
// the Result. Useful for piped or file-based invocations.
func ScanReader(r io.Reader) (Result, error) {
	var in Input
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return Result{}, fmt.Errorf("decode billing input: %w", err)
	}
	return Default().Scan(in), nil
}

// ScanFile reads Input JSON from path, runs the default scanner, and returns
// the Result.
func ScanFile(path string) (Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return Result{}, fmt.Errorf("open billing input file: %w", err)
	}
	defer f.Close()
	return ScanReader(f)
}
