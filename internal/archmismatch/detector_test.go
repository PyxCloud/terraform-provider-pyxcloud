package archmismatch

import (
	"testing"
)

// --- helpers ---

func modulesWithTypes(name string, types ...string) Module {
	return Module{Name: name, ResourceTypes: types}
}

func findingsByRule(findings []Finding, rt RuleType) []Finding {
	var out []Finding
	for _, f := range findings {
		if f.RuleType == rt {
			out = append(out, f)
		}
	}
	return out
}

// --- DefaultRules / Detector basics ---

func TestNewDetectorDefaultRules(t *testing.T) {
	det := NewDetector(DefaultRules())
	if det.RuleCount() != 3 {
		t.Errorf("expected 3 default rules, got %d", det.RuleCount())
	}
}

func TestDetectorAddRule(t *testing.T) {
	det := NewDetector(nil)
	det.AddRule(DefaultPrematureSplitRule)
	if det.RuleCount() != 1 {
		t.Errorf("expected 1 rule after AddRule, got %d", det.RuleCount())
	}
}

func TestDetectorAnalyze_Empty(t *testing.T) {
	det := NewDetector(DefaultRules())
	findings := det.Analyze(nil)
	if len(findings) != 0 {
		t.Errorf("expected 0 findings for nil modules, got %d", len(findings))
	}
}

// --- PrematureSplit ---

func TestPrematureSplit_BelowThreshold(t *testing.T) {
	det := NewDetector([]Rule{DefaultPrematureSplitRule})
	modules := []Module{
		modulesWithTypes("module.tiny", "aws_lambda_function", "aws_iam_role"), // 2 types < 3
	}
	findings := det.Analyze(modules)
	ps := findingsByRule(findings, PrematureSplit)
	if len(ps) != 1 {
		t.Fatalf("expected 1 PrematureSplit finding, got %d", len(ps))
	}
	if ps[0].Module != "module.tiny" {
		t.Errorf("unexpected module name: %s", ps[0].Module)
	}
	if ps[0].Severity != SeverityMedium {
		t.Errorf("expected MEDIUM severity, got %s", ps[0].Severity)
	}
}

func TestPrematureSplit_AtThreshold(t *testing.T) {
	det := NewDetector([]Rule{DefaultPrematureSplitRule})
	modules := []Module{
		modulesWithTypes("module.ok", "aws_lambda_function", "aws_iam_role", "aws_cloudwatch_log_group"), // exactly 3
	}
	findings := det.Analyze(modules)
	ps := findingsByRule(findings, PrematureSplit)
	if len(ps) != 0 {
		t.Errorf("expected 0 PrematureSplit findings at threshold, got %d", len(ps))
	}
}

func TestPrematureSplit_DuplicateTypesNotCounted(t *testing.T) {
	det := NewDetector([]Rule{DefaultPrematureSplitRule})
	// 4 entries but only 2 distinct types
	modules := []Module{
		modulesWithTypes("module.dupe",
			"aws_lambda_function",
			"aws_lambda_function",
			"aws_iam_role",
			"aws_iam_role",
		),
	}
	findings := det.Analyze(modules)
	ps := findingsByRule(findings, PrematureSplit)
	if len(ps) != 1 {
		t.Errorf("expected 1 PrematureSplit finding (duplicates must not pad count), got %d", len(ps))
	}
}

func TestPrematureSplit_DisabledRule(t *testing.T) {
	rule := DefaultPrematureSplitRule
	rule.Enabled = false
	det := NewDetector([]Rule{rule})
	modules := []Module{
		modulesWithTypes("module.tiny", "aws_s3_bucket"),
	}
	if findings := det.Analyze(modules); len(findings) != 0 {
		t.Errorf("expected 0 findings with disabled rule, got %d", len(findings))
	}
}

// --- CopiedLayout ---

func TestCopiedLayout_HighSimilarity(t *testing.T) {
	det := NewDetector([]Rule{DefaultCopiedLayoutRule})
	modules := []Module{
		modulesWithTypes("module.svc-a", "aws_lambda_function", "aws_iam_role", "aws_cloudwatch_log_group"),
		modulesWithTypes("module.svc-b", "aws_lambda_function", "aws_iam_role", "aws_cloudwatch_log_group"),
	}
	findings := det.Analyze(modules)
	cl := findingsByRule(findings, CopiedLayout)
	if len(cl) != 1 {
		t.Fatalf("expected 1 CopiedLayout finding, got %d", len(cl))
	}
	if cl[0].Severity != SeverityHigh {
		t.Errorf("expected HIGH severity, got %s", cl[0].Severity)
	}
}

func TestCopiedLayout_LowSimilarity(t *testing.T) {
	det := NewDetector([]Rule{DefaultCopiedLayoutRule})
	modules := []Module{
		modulesWithTypes("module.frontend", "aws_cloudfront_distribution", "aws_s3_bucket"),
		modulesWithTypes("module.database", "aws_rds_instance", "aws_db_subnet_group", "aws_security_group"),
	}
	findings := det.Analyze(modules)
	cl := findingsByRule(findings, CopiedLayout)
	if len(cl) != 0 {
		t.Errorf("expected 0 CopiedLayout findings for dissimilar modules, got %d", len(cl))
	}
}

func TestCopiedLayout_IdenticalSets(t *testing.T) {
	det := NewDetector([]Rule{DefaultCopiedLayoutRule})
	modules := []Module{
		modulesWithTypes("module.x", "aws_lambda_function"),
		modulesWithTypes("module.y", "aws_lambda_function"),
	}
	findings := det.Analyze(modules)
	cl := findingsByRule(findings, CopiedLayout)
	if len(cl) != 1 {
		t.Fatalf("expected 1 finding for identical sets, got %d", len(cl))
	}
}

func TestCopiedLayout_ThreeModulesTwoPairs(t *testing.T) {
	det := NewDetector([]Rule{DefaultCopiedLayoutRule})
	shared := []string{"aws_lambda_function", "aws_iam_role", "aws_cloudwatch_log_group"}
	modules := []Module{
		{Name: "module.a", ResourceTypes: shared},
		{Name: "module.b", ResourceTypes: shared},
		{Name: "module.c", ResourceTypes: shared},
	}
	findings := det.Analyze(modules)
	cl := findingsByRule(findings, CopiedLayout)
	// 3 choose 2 = 3 pairs
	if len(cl) != 3 {
		t.Errorf("expected 3 CopiedLayout findings for 3 identical modules, got %d", len(cl))
	}
}

// --- CrossBoundaryDependency ---

func TestCrossBoundary_AllowedEdge(t *testing.T) {
	det := NewDetector([]Rule{DefaultCrossBoundaryRule})
	// compute→data is the natural dependency direction: compute calls data layer.
	modules := []Module{
		{Name: "module.db", Tier: "data"},
		{Name: "module.api", Tier: "compute", DependsOn: []string{"module.db"}}, // compute->data: allowed
	}
	findings := det.Analyze(modules)
	cb := findingsByRule(findings, CrossBoundaryDependency)
	if len(cb) != 0 {
		t.Errorf("expected 0 CrossBoundary findings for allowed compute->data edge, got %d", len(cb))
	}
}

func TestCrossBoundary_AllowedPresentationToCompute(t *testing.T) {
	det := NewDetector([]Rule{DefaultCrossBoundaryRule})
	modules := []Module{
		{Name: "module.api", Tier: "compute"},
		{Name: "module.ui", Tier: "presentation", DependsOn: []string{"module.api"}}, // presentation->compute: allowed
	}
	findings := det.Analyze(modules)
	cb := findingsByRule(findings, CrossBoundaryDependency)
	if len(cb) != 0 {
		t.Errorf("expected 0 CrossBoundary findings for allowed presentation->compute edge, got %d", len(cb))
	}
}

func TestCrossBoundary_ForbiddenEdge(t *testing.T) {
	det := NewDetector([]Rule{DefaultCrossBoundaryRule})
	modules := []Module{
		{Name: "module.ui", Tier: "presentation"},
		// data layer directly depending on presentation violates layered architecture
		{Name: "module.db", Tier: "data", DependsOn: []string{"module.ui"}}, // data->presentation: forbidden
	}
	findings := det.Analyze(modules)
	cb := findingsByRule(findings, CrossBoundaryDependency)
	if len(cb) != 1 {
		t.Fatalf("expected 1 CrossBoundary finding, got %d", len(cb))
	}
	if cb[0].Module != "module.db" {
		t.Errorf("expected finding on module.db, got %s", cb[0].Module)
	}
	if cb[0].RelatedModule != "module.ui" {
		t.Errorf("expected related module.ui, got %s", cb[0].RelatedModule)
	}
}

func TestCrossBoundary_ForbiddenSkipTier(t *testing.T) {
	det := NewDetector([]Rule{DefaultCrossBoundaryRule})
	modules := []Module{
		{Name: "module.db", Tier: "data"},
		// presentation skipping compute to depend directly on data: forbidden
		{Name: "module.ui", Tier: "presentation", DependsOn: []string{"module.db"}},
	}
	findings := det.Analyze(modules)
	cb := findingsByRule(findings, CrossBoundaryDependency)
	if len(cb) != 1 {
		t.Fatalf("expected 1 CrossBoundary finding for tier-skip, got %d", len(cb))
	}
}

func TestCrossBoundary_SameTierNoPenalty(t *testing.T) {
	det := NewDetector([]Rule{DefaultCrossBoundaryRule})
	modules := []Module{
		{Name: "module.worker", Tier: "compute"},
		{Name: "module.scheduler", Tier: "compute", DependsOn: []string{"module.worker"}},
	}
	if findings := det.Analyze(modules); len(findings) != 0 {
		t.Errorf("expected 0 findings for same-tier dependency, got %d", len(findings))
	}
}

func TestCrossBoundary_NoTierSkipped(t *testing.T) {
	det := NewDetector([]Rule{DefaultCrossBoundaryRule})
	modules := []Module{
		{Name: "module.x"}, // no tier
		{Name: "module.y", DependsOn: []string{"module.x"}},
	}
	if findings := det.Analyze(modules); len(findings) != 0 {
		t.Errorf("expected 0 findings when tier is unset, got %d", len(findings))
	}
}

// --- helpers unit tests ---

func TestDistinctCount(t *testing.T) {
	cases := []struct {
		in   []string
		want int
	}{
		{nil, 0},
		{[]string{"a"}, 1},
		{[]string{"a", "a"}, 1},
		{[]string{"a", "b", "a"}, 2},
	}
	for _, c := range cases {
		if got := distinctCount(c.in); got != c.want {
			t.Errorf("distinctCount(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestJaccardSimilarity(t *testing.T) {
	cases := []struct {
		a, b []string
		want float64
	}{
		{nil, nil, 1.0},
		{[]string{"a"}, []string{"a"}, 1.0},
		{[]string{"a"}, []string{"b"}, 0.0},
		{[]string{"a", "b"}, []string{"b", "c"}, 1.0 / 3.0},
	}
	for _, c := range cases {
		got := jaccardSimilarity(c.a, c.b)
		if abs(got-c.want) > 0.001 {
			t.Errorf("jaccard(%v,%v) = %f, want %f", c.a, c.b, got, c.want)
		}
	}
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func TestResourceTypeUnion(t *testing.T) {
	a := []string{"aws_lambda_function", "aws_iam_role"}
	b := []string{"aws_iam_role", "aws_s3_bucket"}
	union := resourceTypeUnion(a, b)
	if len(union) != 3 {
		t.Errorf("expected 3 items in union, got %d: %v", len(union), union)
	}
}
