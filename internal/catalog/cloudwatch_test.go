package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestTranslateMonitoringAWS(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	p, err := TranslateMonitoring(context.Background(), cat, MonitoringSpec{
		Name: "obs", Provider: "aws", Region: "Dublin",
		LogGroups: []LogGroup{{Name: "/pyx/app", RetentionDays: 30}},
		Alarms: []MetricAlarm{{
			Name: "cpu-high", Namespace: "AWS/EC2", MetricName: "CPUUtilization",
			ComparisonOperator: "GreaterThanThreshold", Threshold: 80, EvaluationPeriods: 2,
		}},
	})
	if err != nil {
		t.Fatalf("TranslateMonitoring: %v", err)
	}
	hcl, err := RenderMonitoringHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_cloudwatch_log_group\"",
		"name = \"/pyx/app\"",
		"retention_in_days = 30",
		"resource \"aws_cloudwatch_metric_alarm\"",
		"metric_name         = \"CPUUtilization\"",
		"threshold           = 80",
		"evaluation_periods  = 2",
		"period              = 300", // default
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("monitoring HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

// TestTranslateMonitoringDORequiresCluster proves the DO LGTM path fails closed
// (hard plan-time error, no silent fallback) when no cluster is given.
func TestTranslateMonitoringDORequiresCluster(t *testing.T) {
	cat, _ := NewEmbedded()
	_, err := TranslateMonitoring(context.Background(), cat, MonitoringSpec{
		Name: "x", Provider: "digitalocean", Region: "Amsterdam",
		Alarms: []MetricAlarm{{Name: "a", MetricName: "up", ComparisonOperator: "LessThanThreshold", Threshold: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "cluster_name is required") {
		t.Errorf("expected DO cluster_name required error, got %v", err)
	}
}

// TestTranslateMonitoringDOLGTM is the LGTM operator-pattern round-trip: CORE
// upstream operators (kube-prometheus-stack + Loki) via helm_release, EXTRA custom
// resources (ServiceMonitor + PrometheusRule + Grafana datasources), and the
// CloudWatch-alarm -> PrometheusRule-alert mapping.
func TestTranslateMonitoringDOLGTM(t *testing.T) {
	cat, _ := NewEmbedded()
	p, err := TranslateMonitoring(context.Background(), cat, MonitoringSpec{
		Name: "obs", Provider: "digitalocean", Region: "Amsterdam",
		ClusterName: "backend",
		ScrapeTargets: []ScrapeTarget{
			{Name: "backend", MatchLabels: map[string]string{"app": "backend"}, Port: "metrics"},
			{Name: "worker", MatchLabels: map[string]string{"app": "worker"}, Port: "http", PodMonitor: true},
		},
		Alarms: []MetricAlarm{{
			Name: "cpu-high", Namespace: "AWS/EC2", MetricName: "node_cpu_ratio",
			ComparisonOperator: "GreaterThanThreshold", Threshold: 0.8, EvaluationPeriods: 3, PeriodSeconds: 60,
			Severity: "critical",
		}},
		TempoDatasourceName: "app-traces",
	})
	if err != nil {
		t.Fatalf("TranslateMonitoring DO: %v", err)
	}
	if !p.RendersHelm || p.ResourceType != "kubernetes_manifest" {
		t.Fatalf("DO LGTM plan must render helm + kubernetes_manifest, got renders_helm=%v type=%q", p.RendersHelm, p.ResourceType)
	}
	hcl, err := RenderMonitoringHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		// CORE operators (helm_release).
		`resource "helm_release" "obs_kube_prometheus_stack"`,
		`chart      = "kube-prometheus-stack"`,
		`resource "helm_release" "obs_loki"`,
		`chart      = "loki"`,
		// EXTRA: ServiceMonitor + PodMonitor.
		`kind       = "ServiceMonitor"`,
		`kind       = "PodMonitor"`,
		`podMetricsEndpoints = [`,
		// EXTRA: PrometheusRule alert (the CloudWatch alarm mapping).
		`kind       = "PrometheusRule"`,
		`alert = "cpu-high"`,
		`expr  = "node_cpu_ratio > 0.8"`, // GreaterThanThreshold -> >
		`for   = "180s"`,                 // 3 * 60s
		`severity = "critical"`,
		// EXTRA: Grafana datasources (Loki + Tempo operator reuse).
		`name   = "Loki"`,
		`name   = "Tempo"`,
		`tempo-app-traces-tempo-query-frontend`,
		// shared cluster data source.
		`data "digitalocean_kubernetes_cluster" "obs_cluster"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO LGTM HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

// TestPromOperatorMapping pins the CloudWatch->PromQL comparison-operator mapping.
func TestPromOperatorMapping(t *testing.T) {
	cases := map[string]string{
		"GreaterThanThreshold":          ">",
		"GreaterThanOrEqualToThreshold": ">=",
		"LessThanThreshold":             "<",
		"LessThanOrEqualToThreshold":    "<=",
		"SomethingUnknown":              ">", // explicit safe default
	}
	for cw, want := range cases {
		if got := promOperator(cw); got != want {
			t.Errorf("promOperator(%q) = %q, want %q", cw, got, want)
		}
	}
}

func TestAssembleHCLMonitoringComponent(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "obs", Type: "monitoring", Monitoring: &AssembleMonitoring{
				LogGroups: []LogGroup{{Name: "/pyx/demo", RetentionDays: 14}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL monitoring: %v", err)
	}
	if !strings.Contains(strings.Join(docs, "\n"), "aws_cloudwatch_log_group") {
		t.Errorf("assembled monitoring missing log group:\n%s", strings.Join(docs, "\n"))
	}
}
