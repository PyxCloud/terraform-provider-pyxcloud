// Package archmismatch detects premature architectural splitting and
// copied-architecture mismatches in Terraform module graphs.
package archmismatch

import (
	"fmt"
	"sort"
	"strings"
)

// RuleType enumerates the supported architecture-mismatch detection patterns.
type RuleType string

const (
	// PrematureSplit fires when a module is split too early — before it
	// contains enough distinct resource types to justify the boundary.
	PrematureSplit RuleType = "premature_split"

	// CopiedLayout fires when two or more modules share an identical or
	// near-identical set of resource types (Jaccard similarity >= threshold).
	CopiedLayout RuleType = "copied_layout"

	// CrossBoundaryDependency fires when a module depends on another module
	// at a non-adjacent architectural tier.
	CrossBoundaryDependency RuleType = "cross_boundary_dependency"
)

// Severity levels for findings.
const (
	SeverityHigh   = "HIGH"
	SeverityMedium = "MEDIUM"
	SeverityLow    = "LOW"
)

// Module represents a Terraform module node in the architecture graph.
type Module struct {
	// Name is the module's logical name or address (e.g. "module.svc_auth").
	Name string
	// ResourceTypes lists all distinct Terraform resource types declared in
	// the module (e.g. ["aws_lambda_function", "aws_iam_role"]).
	ResourceTypes []string
	// Tier is the optional architectural tier label (e.g. "data", "compute",
	// "presentation").  Required only for CrossBoundaryDependency detection.
	Tier string
	// DependsOn lists the names of modules this module imports or depends on.
	DependsOn []string
}

// Finding describes a single detected architecture mismatch.
type Finding struct {
	// RuleID is the identifier of the rule that fired.
	RuleID string
	// RuleType is the detection pattern that matched.
	RuleType RuleType
	// Module is the name of the primary module involved.
	Module string
	// RelatedModule is the name of the secondary module (for pairwise rules).
	RelatedModule string
	// Severity is "HIGH", "MEDIUM", or "LOW".
	Severity string
	// Message is a human-readable description of the mismatch.
	Message string
	// Remediation is the recommended corrective action.
	Remediation string
}

// Rule defines a single architecture-mismatch detection rule.
type Rule struct {
	// ID is a unique identifier for the rule.
	ID string
	// Type is the detection pattern.
	Type RuleType
	// Threshold is the numeric trigger value:
	//   PrematureSplit:          minimum distinct resource types per sub-module
	//   CopiedLayout:            Jaccard similarity [0,1]
	//   CrossBoundaryDependency: not used (set to 0)
	Threshold float64
	// Severity of generated findings.
	Severity string
	// Enabled controls whether the rule is active.
	Enabled bool
	// Description provides human-readable context.
	Description string
	// AllowedTierEdges lists permitted (from→to) tier pairs for
	// CrossBoundaryDependency detection.  Pairs are "fromTier->toTier".
	AllowedTierEdges []string
}

// Built-in architecture-mismatch rules.
var (
	// DefaultPrematureSplitRule flags any module with fewer than 3 distinct
	// resource types — a strong signal that the split is premature.
	DefaultPrematureSplitRule = Rule{
		ID:          "premature-split-lt3",
		Type:        PrematureSplit,
		Threshold:   3,
		Severity:    SeverityMedium,
		Enabled:     true,
		Description: "Module has fewer than 3 distinct resource types — likely a premature split",
	}

	// DefaultCopiedLayoutRule flags pairs of modules whose resource-type sets
	// have a Jaccard similarity >= 0.85.
	DefaultCopiedLayoutRule = Rule{
		ID:          "copied-layout-jaccard-085",
		Type:        CopiedLayout,
		Threshold:   0.85,
		Severity:    SeverityHigh,
		Enabled:     true,
		Description: "Two modules share ≥85% resource-type overlap — likely copied architecture",
	}

	// DefaultCrossBoundaryRule allows compute→data and presentation→compute
	// dependencies (the natural call-direction in a layered architecture).
	// All other cross-tier edges are flagged.
	//
	// Edge format is "callerTier->calleeTier" (the direction of the DependsOn
	// relationship, i.e. the module that imports the other).
	DefaultCrossBoundaryRule = Rule{
		ID:       "cross-boundary-layered",
		Type:     CrossBoundaryDependency,
		Severity: SeverityHigh,
		Enabled:  true,
		AllowedTierEdges: []string{
			"compute->data",           // compute layer may depend on data layer
			"presentation->compute",   // presentation layer may depend on compute layer
		},
		Description: "Module depends on a non-adjacent architectural tier",
	}
)

// Detector runs a set of Rules against a module graph and returns findings.
type Detector struct {
	rules []Rule
}

// NewDetector creates a Detector with the given rule set.
func NewDetector(rules []Rule) *Detector {
	return &Detector{rules: rules}
}

// DefaultRules returns a copy of the three built-in architecture-mismatch rules.
func DefaultRules() []Rule {
	return []Rule{
		DefaultPrematureSplitRule,
		DefaultCopiedLayoutRule,
		DefaultCrossBoundaryRule,
	}
}

// AddRule appends a rule to the detector's active rule set.
func (d *Detector) AddRule(rule Rule) {
	d.rules = append(d.rules, rule)
}

// RuleCount returns the number of rules currently registered.
func (d *Detector) RuleCount() int {
	return len(d.rules)
}

// Analyze runs all active rules against the provided modules and returns
// every finding.  An empty slice means no mismatches were detected.
func (d *Detector) Analyze(modules []Module) []Finding {
	var findings []Finding
	for i := range d.rules {
		r := &d.rules[i]
		if !r.Enabled {
			continue
		}
		switch r.Type {
		case PrematureSplit:
			findings = append(findings, r.evalPrematureSplit(modules)...)
		case CopiedLayout:
			findings = append(findings, r.evalCopiedLayout(modules)...)
		case CrossBoundaryDependency:
			findings = append(findings, r.evalCrossBoundary(modules)...)
		}
	}
	return findings
}

// evalPrematureSplit checks each module for too few distinct resource types.
func (r *Rule) evalPrematureSplit(modules []Module) []Finding {
	var findings []Finding
	for _, m := range modules {
		distinct := distinctCount(m.ResourceTypes)
		if float64(distinct) < r.Threshold {
			findings = append(findings, Finding{
				RuleID:      r.ID,
				RuleType:    r.Type,
				Module:      m.Name,
				Severity:    r.Severity,
				Message:     fmt.Sprintf("module %q has only %d distinct resource type(s) — threshold is %.0f", m.Name, distinct, r.Threshold),
				Remediation: "Consolidate this module with its parent until at least 3 distinct resource type clusters with independent lifecycles are identified.",
			})
		}
	}
	return findings
}

// evalCopiedLayout checks all module pairs for high resource-type similarity.
func (r *Rule) evalCopiedLayout(modules []Module) []Finding {
	var findings []Finding
	for i := 0; i < len(modules); i++ {
		for j := i + 1; j < len(modules); j++ {
			a, b := modules[i], modules[j]
			sim := jaccardSimilarity(a.ResourceTypes, b.ResourceTypes)
			if sim >= r.Threshold {
				findings = append(findings, Finding{
					RuleID:        r.ID,
					RuleType:      r.Type,
					Module:        a.Name,
					RelatedModule: b.Name,
					Severity:      r.Severity,
					Message:       fmt.Sprintf("modules %q and %q have %.0f%% resource-type overlap (Jaccard=%.2f) — possible copied architecture", a.Name, b.Name, sim*100, sim),
					Remediation:   "Extract a shared base module that both services import, or verify whether these services should be unified.",
				})
			}
		}
	}
	return findings
}

// evalCrossBoundary checks module dependency edges for tier violations.
func (r *Rule) evalCrossBoundary(modules []Module) []Finding {
	// Build a quick lookup: module name → tier
	tierOf := make(map[string]string, len(modules))
	for _, m := range modules {
		tierOf[m.Name] = m.Tier
	}

	allowed := make(map[string]bool, len(r.AllowedTierEdges))
	for _, e := range r.AllowedTierEdges {
		allowed[e] = true
	}

	var findings []Finding
	for _, m := range modules {
		if m.Tier == "" {
			continue
		}
		for _, dep := range m.DependsOn {
			depTier, ok := tierOf[dep]
			if !ok || depTier == "" || depTier == m.Tier {
				continue
			}
			edge := m.Tier + "->" + depTier
			if !allowed[edge] {
				findings = append(findings, Finding{
					RuleID:        r.ID,
					RuleType:      r.Type,
					Module:        m.Name,
					RelatedModule: dep,
					Severity:      r.Severity,
					Message:       fmt.Sprintf("module %q (tier=%s) depends on %q (tier=%s) — cross-boundary dependency violates layered architecture", m.Name, m.Tier, dep, depTier),
					Remediation:   "Introduce an interface layer (SSM parameter, published output, or API contract) between the tiers instead of a direct module dependency.",
				})
			}
		}
	}
	return findings
}

// distinctCount returns the number of unique strings in s.
func distinctCount(s []string) int {
	seen := make(map[string]struct{}, len(s))
	for _, v := range s {
		seen[strings.TrimSpace(v)] = struct{}{}
	}
	return len(seen)
}

// jaccardSimilarity computes |A∩B| / |A∪B| for two string slices.
func jaccardSimilarity(a, b []string) float64 {
	setA := toSet(a)
	setB := toSet(b)
	if len(setA) == 0 && len(setB) == 0 {
		return 1.0
	}
	intersection := 0
	for k := range setA {
		if setB[k] {
			intersection++
		}
	}
	union := len(setA) + len(setB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func toSet(s []string) map[string]bool {
	m := make(map[string]bool, len(s))
	for _, v := range s {
		m[strings.TrimSpace(v)] = true
	}
	return m
}

// sortedKeys returns sorted keys of a string-bool map (for deterministic output).
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// resourceTypeUnion returns the union of two resource-type slices (deduplicated).
func resourceTypeUnion(a, b []string) []string {
	merged := toSet(append(a, b...))
	return sortedKeys(merged)
}
