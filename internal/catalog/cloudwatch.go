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
//     This is the managed CloudWatch + SNS pairing being migrated AWAY from (the
//     AWS peer we keep).
//   - GCP: google_logging_project_bucket_config + google_monitoring_alert_policy
//     (best-effort shape; emitted only for log retention + threshold alarms).
//   - DigitalOcean: the canonical LGTM observability stack on DOKS, expressed with
//     the Kubernetes OPERATOR pattern (pd-MIG-LGTM-MONITORING / operator.go).
//     CORE = upstream operators via helm_release: the kube-prometheus-stack
//     (Prometheus Operator + Grafana + Alertmanager) and grafana/loki. EXTRA =
//     our custom resources: ServiceMonitor / PodMonitor (scrape targets),
//     PrometheusRule (the alerts that REPLACE the CloudWatch metric alarms, routed
//     through Alertmanager instead of SNS), and Grafana datasources (Loki for
//     logs, Tempo — reusing the tracing component's Tempo operator — for traces).
//     DO has no managed CloudWatch/SNS equivalent, so the OSS stack is the
//     replacement; rendered as operators + CRs, never hand-rolled Deployments.
//
// AWS-first historically; the DO LGTM path is the AWS->DO migration target.

// LogGroup is one log group with optional retention (days; 0 = never expire).
type LogGroup struct {
	Name          string
	RetentionDays int
}

// MetricAlarm is one threshold alarm on a metric. On AWS it renders an
// aws_cloudwatch_metric_alarm; on DO it is translated into a PrometheusRule alert
// (the CloudWatch->Prometheus mapping). The same abstract fields drive both peers:
//
//	CloudWatch metric alarm            ->  PrometheusRule alert
//	------------------------------------   ----------------------------------------
//	alarm_name                         ->  alert: <Name>
//	metric_name (+ namespace)          ->  the PromQL series (PromQL override or
//	                                        the metric_name mapped to a metric)
//	comparison_operator + threshold    ->  expr: <metric> <op> <threshold>
//	evaluation_periods * period        ->  for: <duration>
//	(SNS notification action)          ->  Alertmanager routing (replaces SNS)
type MetricAlarm struct {
	Name               string
	Namespace          string // e.g. AWS/EC2
	MetricName         string // e.g. CPUUtilization
	ComparisonOperator string // e.g. GreaterThanThreshold
	Threshold          float64
	EvaluationPeriods  int
	PeriodSeconds      int
	Statistic          string // e.g. Average

	// PromQL optionally overrides the metric expression for the DO/Prometheus peer.
	// When empty the DO path derives a PromQL series from MetricName. Ignored on AWS.
	PromQL string
	// Severity tags the Prometheus alert (e.g. "warning", "critical"). Empty ->
	// "warning". Ignored on AWS. Drives Alertmanager routing (the SNS replacement).
	Severity string
}

// ScrapeTarget is one Prometheus scrape target rendered as a ServiceMonitor (or a
// PodMonitor when PodLabels is set) custom resource — the EXTRA half of the LGTM
// operator pattern, telling the Prometheus Operator what to scrape. DO-only.
type ScrapeTarget struct {
	Name string // CR name, e.g. "backend"
	// MatchLabels selects the Service (ServiceMonitor) or Pods (PodMonitor) to scrape.
	MatchLabels map[string]string
	// Port is the named endpoint port to scrape (e.g. "metrics", "http").
	Port string
	// Path is the metrics path (empty -> "/metrics").
	Path string
	// Interval is the scrape interval (empty -> "30s").
	Interval string
	// PodMonitor selects a PodMonitor instead of a ServiceMonitor when true.
	PodMonitor bool
}

// MonitoringSpec is the abstract monitoring description.
type MonitoringSpec struct {
	Name      string
	Region    string
	Provider  string
	LogGroups []LogGroup
	Alarms    []MetricAlarm

	// ── LGTM stack (DigitalOcean) ──
	// ClusterName is the existing DOKS cluster the LGTM stack runs on. Required for
	// DO; ignored on AWS/GCP.
	ClusterName string
	// Namespace is the Kubernetes namespace for the LGTM workloads. Empty ->
	// "observability" (shared with the tracing component so Grafana can reach Tempo).
	Namespace string
	// ScrapeTargets are the ServiceMonitor/PodMonitor CRs (what Prometheus scrapes).
	ScrapeTargets []ScrapeTarget
	// TempoDatasourceName, when set, wires a Grafana Tempo datasource pointing at the
	// tracing component's TempoStack query-frontend (operator reuse). Empty -> skip.
	TempoDatasourceName string
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

	// ── LGTM stack (DigitalOcean) ──
	ClusterName         string         `json:"cluster_name,omitempty"`
	Namespace           string         `json:"namespace,omitempty"`
	ScrapeTargets       []ScrapeTarget `json:"scrape_targets,omitempty"`
	TempoDatasourceName string         `json:"tempo_datasource_name,omitempty"`

	// RendersHelm is true when the render emits a helm_release (the operator-pattern
	// CORE — kube-prometheus-stack + grafana/loki). assemble.go pins hashicorp/helm
	// (needsHelm) when set, mirroring how kubernetes_manifest drives needsKubernetes.
	RendersHelm bool `json:"renders_helm,omitempty"`
}

// Operator-pattern CORE charts for the DO LGTM stack (pd-MIG-LGTM-MONITORING).
// CORE controllers + CRDs come from the upstream Helm charts; we render the EXTRA
// custom resources (ServiceMonitor/PodMonitor, PrometheusRule, Grafana
// datasources). Chart repos/versions are pinned so the rendered plan is deterministic.
const (
	defaultLGTMNS = "observability"

	kubePromStackRepo    = "https://prometheus-community.github.io/helm-charts"
	kubePromStackChart   = "kube-prometheus-stack"
	kubePromStackVersion = "61.3.2"

	lokiRepo    = "https://grafana.github.io/helm-charts"
	lokiChart   = "loki"
	lokiVersion = "6.6.4"
)

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
	case ProviderDigitalOcean:
		// The LGTM operator-pattern stack on DOKS — the canonical CloudWatch + SNS
		// replacement (DO has no managed monitoring/notification service).
		cluster := strings.TrimSpace(spec.ClusterName)
		if cluster == "" {
			return MonitoringPlan{}, fmt.Errorf(
				"monitoring: digitalocean replaces CloudWatch+SNS with the LGTM stack " +
					"(kube-prometheus-stack + Loki + Grafana + Alertmanager) on a DOKS cluster " +
					"(DO has no managed monitoring service) — cluster_name is required. This is a " +
					"hard plan-time error, never a silent fallback")
		}
		ns := strings.TrimSpace(spec.Namespace)
		if ns == "" {
			ns = defaultLGTMNS
		}
		for _, st := range spec.ScrapeTargets {
			if strings.TrimSpace(st.Name) == "" {
				return MonitoringPlan{}, fmt.Errorf("monitoring: scrape_target name is required")
			}
			if strings.TrimSpace(st.Port) == "" {
				return MonitoringPlan{}, fmt.Errorf("monitoring: scrape_target %q port is required", st.Name)
			}
		}
		plan.ClusterName = cluster
		plan.Namespace = ns
		plan.ScrapeTargets = spec.ScrapeTargets
		plan.TempoDatasourceName = strings.TrimSpace(spec.TempoDatasourceName)
		// Operator pattern: CORE = kube-prometheus-stack + grafana/loki (helm_release);
		// EXTRA = ServiceMonitor/PodMonitor + PrometheusRule + Grafana datasource CRs.
		plan.RendersHelm = true
		plan.ResourceType = "kubernetes_manifest"
	}
	return plan, nil
}
