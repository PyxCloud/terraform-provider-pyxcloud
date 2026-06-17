package catalog

import (
	"fmt"
	"strings"
)

// LogGroupSpec is one log group/sink: a name + optional retention in days.
type LogGroupSpec struct {
	Name          string
	RetentionDays int // 0 = provider default (never-expire on AWS)
}

// AlarmSpec is one metric alarm. Fields mirror the cross-provider-common metric
// alarm shape (namespace/metric/statistic/comparison/threshold/period/evals).
type AlarmSpec struct {
	Name               string
	Namespace          string // e.g. AWS/EC2
	MetricName         string // e.g. CPUUtilization
	Statistic          string // Average | Sum | Maximum | Minimum
	ComparisonOperator string // canonical: gt | gte | lt | lte
	Threshold          float64
	PeriodSeconds      int
	EvaluationPeriods  int
}

// ObservabilitySpec is the canonical logging+alarms component: a set of log
// groups and metric alarms. AWS-complete (CloudWatch); other providers map what
// they cleanly can or surface a hard "unsupported" error (SPEC §1).
type ObservabilitySpec struct {
	Name      string
	Provider  string
	LogGroups []LogGroupSpec
	Alarms    []AlarmSpec
}

// ObservabilityPlan is the deterministic concrete translation.
type ObservabilityPlan struct {
	Provider     string         `json:"provider"`
	CSP          string         `json:"csp"`
	Name         string         `json:"name"`
	LogGroups    []LogGroupSpec `json:"log_groups"`
	Alarms       []AlarmSpec    `json:"alarms"`
	ResourceType string         `json:"resource_type"`
}

// awsComparisonOperator maps the canonical operator to the CloudWatch enum.
var awsComparisonOperator = map[string]string{
	"gt":  "GreaterThanThreshold",
	"gte": "GreaterThanOrEqualToThreshold",
	"lt":  "LessThanThreshold",
	"lte": "LessThanOrEqualToThreshold",
}

// TranslateObservability resolves an ObservabilitySpec into a concrete plan.
// Global (no region/SKU lookup). AWS is fully supported; other providers are a
// hard "unsupported" error until their logging/monitoring mapping is built.
func TranslateObservability(spec ObservabilitySpec) (ObservabilityPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return ObservabilityPlan{}, fmt.Errorf("observability: name is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return ObservabilityPlan{}, fmt.Errorf("observability: unknown provider %q", spec.Provider)
	}
	if len(spec.LogGroups) == 0 && len(spec.Alarms) == 0 {
		return ObservabilityPlan{}, fmt.Errorf("observability: declare at least one log_group or alarm")
	}
	for _, lg := range spec.LogGroups {
		if strings.TrimSpace(lg.Name) == "" {
			return ObservabilityPlan{}, fmt.Errorf("observability: log_group needs a name")
		}
	}
	for _, a := range spec.Alarms {
		if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.MetricName) == "" {
			return ObservabilityPlan{}, fmt.Errorf("observability: alarm %q needs a name and metric_name", a.Name)
		}
		op := strings.ToLower(strings.TrimSpace(a.ComparisonOperator))
		if _, ok := awsComparisonOperator[op]; !ok {
			return ObservabilityPlan{}, fmt.Errorf("observability: alarm %q has invalid comparison %q (gt|gte|lt|lte)", a.Name, a.ComparisonOperator)
		}
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	plan := ObservabilityPlan{Provider: provider, CSP: csp, Name: spec.Name, LogGroups: spec.LogGroups, Alarms: spec.Alarms}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_cloudwatch_log_group"
	default:
		return ObservabilityPlan{}, fmt.Errorf("observability: unsupported on provider %q (supported: aws CloudWatch). "+
			"Hard plan-time error, never an invented resource", provider)
	}
	return plan, nil
}

// RenderObservabilityHCL renders a resolved ObservabilityPlan.
func RenderObservabilityHCL(plan ObservabilityPlan) (string, error) {
	if plan.Provider != ProviderAWS {
		return "", fmt.Errorf("observability: no renderer for provider %q", plan.Provider)
	}
	var b strings.Builder
	for _, lg := range plan.LogGroups {
		rn := tfName(plan.Name + "-" + lg.Name)
		fmt.Fprintf(&b, "resource \"aws_cloudwatch_log_group\" %q {\n", rn)
		fmt.Fprintf(&b, "  name = %q\n", lg.Name)
		if lg.RetentionDays > 0 {
			fmt.Fprintf(&b, "  retention_in_days = %d\n", lg.RetentionDays)
		}
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
	}
	for _, a := range plan.Alarms {
		rn := tfName(plan.Name + "-" + a.Name)
		op := awsComparisonOperator[strings.ToLower(strings.TrimSpace(a.ComparisonOperator))]
		period := a.PeriodSeconds
		if period <= 0 {
			period = 300
		}
		evals := a.EvaluationPeriods
		if evals <= 0 {
			evals = 1
		}
		stat := a.Statistic
		if stat == "" {
			stat = "Average"
		}
		fmt.Fprintf(&b, "resource \"aws_cloudwatch_metric_alarm\" %q {\n", rn)
		fmt.Fprintf(&b, "  alarm_name          = %q\n", a.Name)
		fmt.Fprintf(&b, "  namespace           = %q\n", a.Namespace)
		fmt.Fprintf(&b, "  metric_name         = %q\n", a.MetricName)
		fmt.Fprintf(&b, "  statistic           = %q\n", stat)
		fmt.Fprintf(&b, "  comparison_operator = %q\n", op)
		fmt.Fprintf(&b, "  threshold           = %g\n", a.Threshold)
		fmt.Fprintf(&b, "  period              = %d\n", period)
		fmt.Fprintf(&b, "  evaluation_periods  = %d\n", evals)
		fmt.Fprintf(&b, "  tags = { pyxcloud = \"true\" }\n")
		b.WriteString("}\n\n")
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}
