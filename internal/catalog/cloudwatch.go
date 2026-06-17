package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Monitoring is the abstract `monitoring` component: log groups + metric alarms —
// the canonical form of the per-provider scripts' aws_cloudwatch_log_group /
// aws_cloudwatch_metric_alarm glue.
//
//   - AWS: aws_cloudwatch_log_group (+ retention) + aws_cloudwatch_metric_alarm.
//   - GCP: google_logging_project_bucket_config + google_monitoring_alert_policy
//     (best-effort shape; emitted only for log retention + threshold alarms).
//   - DigitalOcean: UNSUPPORTED (no first-class log-group/metric-alarm primitive of
//     this form). Clean plan-time error.
//
// AWS-first: GCP/DO fidelity grows as our repos need it; today only AWS is exercised.

// LogGroup is one log group with optional retention (days; 0 = never expire).
type LogGroup struct {
	Name          string
	RetentionDays int
}

// MetricAlarm is one threshold alarm on a metric.
type MetricAlarm struct {
	Name               string
	Namespace          string // e.g. AWS/EC2
	MetricName         string // e.g. CPUUtilization
	ComparisonOperator string // e.g. GreaterThanThreshold
	Threshold          float64
	EvaluationPeriods  int
	PeriodSeconds      int
	Statistic          string // e.g. Average
}

// MonitoringSpec is the abstract monitoring description.
type MonitoringSpec struct {
	Name      string
	Region    string
	Provider  string
	LogGroups []LogGroup
	Alarms    []MetricAlarm
}

// MonitoringPlan is the resolved concrete monitoring translation.
type MonitoringPlan struct {
	Provider     string        `json:"provider"`
	CSP          string        `json:"csp"`
	RegionName   string        `json:"region_name"`
	CSPRegion    string        `json:"csp_region"`
	Name         string        `json:"name"`
	LogGroups    []LogGroup    `json:"log_groups"`
	Alarms       []MetricAlarm `json:"alarms"`
	ResourceType string        `json:"resource_type"`
}

// TranslateMonitoring resolves a MonitoringSpec. DO is unsupported.
func TranslateMonitoring(ctx context.Context, cat RegionCatalog, spec MonitoringSpec) (MonitoringPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return MonitoringPlan{}, fmt.Errorf("monitoring: name is required")
	}
	if len(spec.LogGroups) == 0 && len(spec.Alarms) == 0 {
		return MonitoringPlan{}, fmt.Errorf("monitoring: at least one log_group or alarm is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return MonitoringPlan{}, fmt.Errorf("monitoring: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if strings.EqualFold(spec.Provider, ProviderDigitalOcean) {
		return MonitoringPlan{}, fmt.Errorf("monitoring: unsupported on digitalocean (no log-group/metric-alarm " +
			"primitive of this form) — use AWS/GCP, or self-host monitoring on a VM (hard plan-time error)")
	}
	for _, lg := range spec.LogGroups {
		if strings.TrimSpace(lg.Name) == "" {
			return MonitoringPlan{}, fmt.Errorf("monitoring: log_group name is required")
		}
		if lg.RetentionDays < 0 {
			return MonitoringPlan{}, fmt.Errorf("monitoring: log_group %q retention_days must be >= 0 (0 = never expire)", lg.Name)
		}
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return MonitoringPlan{}, err
	}
	plan := MonitoringPlan{
		Provider: strings.ToLower(spec.Provider), CSP: csp,
		RegionName: row.RegionName, CSPRegion: row.CSPRegion,
		Name: spec.Name, LogGroups: spec.LogGroups, Alarms: spec.Alarms,
	}
	switch plan.Provider {
	case ProviderAWS:
		plan.ResourceType = "aws_cloudwatch_log_group"
	case ProviderGCP:
		plan.ResourceType = "google_logging_project_bucket_config"
	}
	return plan, nil
}
