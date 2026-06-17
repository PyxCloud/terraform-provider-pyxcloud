package catalog

import "testing"

func TestTranslateObservabilityAWS(t *testing.T) {
	plan, err := TranslateObservability(ObservabilitySpec{
		Name:     "backend",
		Provider: "aws",
		LogGroups: []LogGroupSpec{
			{Name: "/pyx/backend", RetentionDays: 30},
			{Name: "/pyx/sast"}, // no retention -> omitted
		},
		Alarms: []AlarmSpec{
			{Name: "cpu-high", Namespace: "AWS/EC2", MetricName: "CPUUtilization",
				Statistic: "Average", ComparisonOperator: "gt", Threshold: 80, PeriodSeconds: 300, EvaluationPeriods: 2},
		},
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	hcl, err := RenderObservabilityHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_cloudwatch_log_group\"",
		"name = \"/pyx/backend\"",
		"retention_in_days = 30",
		"resource \"aws_cloudwatch_metric_alarm\"",
		"comparison_operator = \"GreaterThanThreshold\"",
		"metric_name         = \"CPUUtilization\"",
		"threshold           = 80",
	} {
		if !contains(hcl, want) {
			t.Errorf("observability HCL missing %q\n---\n%s", want, hcl)
		}
	}
	// The retention-less log group must omit retention_in_days for its block.
	if countOccurrences(hcl, "retention_in_days") != 1 {
		t.Errorf("expected exactly 1 retention_in_days (only the 30-day group):\n%s", hcl)
	}
}

func TestTranslateObservabilityValidation(t *testing.T) {
	if _, err := TranslateObservability(ObservabilitySpec{Provider: "aws"}); err == nil {
		t.Error("expected error: name required")
	}
	if _, err := TranslateObservability(ObservabilitySpec{Name: "x", Provider: "aws"}); err == nil {
		t.Error("expected error: need at least one log_group or alarm")
	}
	if _, err := TranslateObservability(ObservabilitySpec{Name: "x", Provider: "aws",
		Alarms: []AlarmSpec{{Name: "a", MetricName: "M", ComparisonOperator: "??"}}}); err == nil {
		t.Error("expected error: invalid comparison operator")
	}
	if _, err := TranslateObservability(ObservabilitySpec{Name: "x", Provider: "digitalocean",
		LogGroups: []LogGroupSpec{{Name: "l"}}}); err == nil {
		t.Error("expected error: unsupported provider")
	}
}

func countOccurrences(s, sub string) int {
	n, i := 0, 0
	for {
		j := indexOf(s[i:], sub)
		if j < 0 {
			return n
		}
		n++
		i += j + len(sub)
	}
}
