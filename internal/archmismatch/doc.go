// Package archmismatch detects premature architectural splitting and
// copied-architecture mismatches in Terraform module graphs.
//
// # Overview
//
// One of the most common — and costly — IaC anti-patterns is "copy-paste
// architecture": a team copies an existing module layout into a new service
// before the service's boundaries are well understood.  The result is
// structural coupling disguised as modularity.  This package provides static
// detection rules that can be run at plan time (or in CI) to flag these
// patterns before they reach production.
//
// # Detection Rules
//
// The detector supports the following rule types (see RuleType constants):
//
//   - PrematureSplit – a module has been split into sub-modules before it
//     contains enough distinct resource types to justify the split (default
//     threshold: <3 distinct resource types per sub-module).
//
//   - CopiedLayout – two or more modules share an identical or near-identical
//     set of resource types, strongly suggesting a copy-paste origin rather
//     than deliberate reuse.  Similarity is measured by Jaccard index
//     (default threshold: ≥0.85).
//
//   - CrossBoundaryDependency – a module at one architectural tier (e.g.,
//     "data") directly depends on a module at a non-adjacent tier (e.g.,
//     "presentation"), violating layered-architecture rules.
//
// # Built-in Rules
//
// Three ready-to-use rules are exported:
//
//   - DefaultPrematureSplitRule
//   - DefaultCopiedLayoutRule
//   - DefaultCrossBoundaryRule
//
// # Usage
//
//	modules := []archmismatch.Module{
//	    {Name: "svc-a", ResourceTypes: []string{"aws_lambda_function", "aws_iam_role"}, Tier: "compute"},
//	    {Name: "svc-b", ResourceTypes: []string{"aws_lambda_function", "aws_iam_role"}, Tier: "compute"},
//	}
//	det := archmismatch.NewDetector(archmismatch.DefaultRules())
//	findings := det.Analyze(modules)
//	for _, f := range findings {
//	    fmt.Printf("[%s] %s: %s\n", f.Severity, f.Module, f.Message)
//	}
//
// # Remediation
//
// PrematureSplit: consolidate the sub-modules back into a single module until
// the service boundary is proven in production.  Introduce the split only
// when you have at least 3 distinct resource type clusters with independent
// lifecycle requirements.
//
// CopiedLayout: extract a shared base module and have both services import it.
// If the two services truly have identical infrastructure, consider whether
// they are actually one service.
//
// CrossBoundaryDependency: introduce an interface layer (e.g., an SSM
// parameter, a published ARN output) between the tiers.  Never let a data
// module import presentation resources directly.
package archmismatch
