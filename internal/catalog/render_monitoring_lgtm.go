package catalog

import (
	"fmt"
	"sort"
	"strings"
)

// render_monitoring_lgtm.go renders the DigitalOcean `monitoring` peer: the LGTM
// observability stack on DOKS, following the Kubernetes OPERATOR pattern
// (pd-MIG-LGTM-MONITORING / operator.go). It is the canonical CloudWatch + SNS
// replacement (DO has no managed monitoring/notification service):
//
//   - CORE (upstream, via renderOperatorComponent): the kube-prometheus-stack chart
//     (the Prometheus Operator + Grafana + Alertmanager + node-exporter, controllers
//     and CRDs all owned upstream) and the grafana/loki chart (log aggregation, the
//     CloudWatch Logs replacement). Installed as helm_release — we never hand-roll
//     the Prometheus/Grafana/Alertmanager Deployments.
//   - EXTRA (ours): the custom resources the operators reconcile —
//       * ServiceMonitor / PodMonitor: what Prometheus scrapes.
//       * PrometheusRule: the alerts that REPLACE the CloudWatch metric alarms;
//         they fire through Alertmanager instead of SNS.
//       * Grafana datasources (GrafanaDatasource CRs): Loki for logs and, when wired,
//         the tracing component's Tempo (operator reuse) for traces — so Grafana is
//         the single pane over the whole LGTM stack.
//
// All resources land on the existing DOKS cluster via the shared
// `data "digitalocean_kubernetes_cluster"` reference used by the other DOKS paths.

// renderMonitoringDO renders the full LGTM operator-pattern component.
func renderMonitoringDO(p MonitoringPlan) string {
	name := tfName(p.Name)
	clusterData := name + "_cluster"
	promStack := name + "_kube_prometheus_stack"
	loki := name + "_loki"

	// ── CORE: upstream operators (controllers + CRDs) via their Helm charts ──
	core := []HelmReleaseSpec{
		{
			TFName:          promStack,
			ReleaseName:     p.Name + "-kube-prometheus-stack",
			Repository:      kubePromStackRepo,
			Chart:           kubePromStackChart,
			Version:         kubePromStackVersion,
			Namespace:       p.Namespace,
			CreateNamespace: true,
			// Let the Prometheus Operator discover ServiceMonitors/PodMonitors/Rules in
			// any namespace (not only those labelled by the release) so our EXTRA CRs
			// are picked up. Deterministic chart values.
			Set: []HelmSet{
				{Name: "prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues", Value: "false"},
				{Name: "prometheus.prometheusSpec.podMonitorSelectorNilUsesHelmValues", Value: "false"},
				{Name: "prometheus.prometheusSpec.ruleSelectorNilUsesHelmValues", Value: "false"},
			},
			ClusterDataRef: clusterData,
		},
		{
			TFName:          loki,
			ReleaseName:     p.Name + "-loki",
			Repository:      lokiRepo,
			Chart:           lokiChart,
			Version:         lokiVersion,
			Namespace:       p.Namespace,
			CreateNamespace: true,
			// Single-binary mode keeps the log backend lightweight (the CloudWatch Logs
			// replacement); filesystem storage is bounded by the chart defaults.
			Set: []HelmSet{
				{Name: "deploymentMode", Value: "SingleBinary"},
				{Name: "loki.commonConfig.replication_factor", Value: "1"},
				{Name: "loki.storage.type", Value: "filesystem"},
			},
			ClusterDataRef: clusterData,
		},
	}

	// ── EXTRA: our custom resources the operators reconcile ──
	var extra []ManifestCR

	// ServiceMonitor / PodMonitor CRs — what Prometheus scrapes.
	for _, st := range p.ScrapeTargets {
		extra = append(extra, ManifestCR{
			TFName:    name + "_scrape_" + tfName(st.Name),
			Manifest:  renderScrapeMonitorManifest(p, st),
			DependsOn: []string{"helm_release." + promStack},
		})
	}

	// PrometheusRule — the alerts that replace the CloudWatch metric alarms (routed
	// through Alertmanager instead of SNS). One rule group holds all alerts.
	if len(p.Alarms) > 0 {
		extra = append(extra, ManifestCR{
			TFName:    name + "_alerts",
			Manifest:  renderPrometheusRuleManifest(p),
			DependsOn: []string{"helm_release." + promStack},
		})
	}

	// Grafana datasources — Loki (logs) and, when wired, the tracing component's Tempo
	// (operator reuse) for traces. Rendered as GrafanaDatasource CRs the
	// kube-prometheus-stack Grafana sidecar / Grafana Operator reconciles.
	extra = append(extra, ManifestCR{
		TFName:    name + "_ds_loki",
		Manifest:  renderLokiDatasourceManifest(p, loki),
		DependsOn: []string{"helm_release." + promStack, "helm_release." + loki},
	})
	if p.TempoDatasourceName != "" {
		extra = append(extra, ManifestCR{
			TFName:    name + "_ds_tempo",
			Manifest:  renderTempoDatasourceManifest(p),
			DependsOn: []string{"helm_release." + promStack},
		})
	}

	return renderOperatorComponent(clusterData, p.ClusterName, core, extra)
}

// renderScrapeMonitorManifest renders one ServiceMonitor (or PodMonitor when
// st.PodMonitor) CR body — the monitoring.coreos.com API the Prometheus Operator
// owns. Tells Prometheus what to scrape.
func renderScrapeMonitorManifest(p MonitoringPlan, st ScrapeTarget) string {
	kind := "ServiceMonitor"
	selectorKey := "selector"
	endpointsKey := "endpoints"
	if st.PodMonitor {
		kind = "PodMonitor"
		selectorKey = "selector"
		endpointsKey = "podMetricsEndpoints"
	}
	path := st.Path
	if path == "" {
		path = "/metrics"
	}
	interval := st.Interval
	if interval == "" {
		interval = "30s"
	}

	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"monitoring.coreos.com/v1\"\n")
	fmt.Fprintf(&b, "    kind       = %q\n", kind)
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-"+st.Name)
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"pyx-monitoring\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	fmt.Fprintf(&b, "      %s = {\n", selectorKey)
	b.WriteString("        matchLabels = {\n")
	for _, k := range sortedKeys(st.MatchLabels) {
		fmt.Fprintf(&b, "          %q = %q\n", k, st.MatchLabels[k])
	}
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	fmt.Fprintf(&b, "      %s = [\n", endpointsKey)
	b.WriteString("        {\n")
	fmt.Fprintf(&b, "          port     = %q\n", st.Port)
	fmt.Fprintf(&b, "          path     = %q\n", path)
	fmt.Fprintf(&b, "          interval = %q\n", interval)
	b.WriteString("        }\n")
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// renderPrometheusRuleManifest renders the PrometheusRule CR body — the alerts that
// replace the CloudWatch metric alarms. Each MetricAlarm becomes one Prometheus
// alert rule; Alertmanager (shipped by kube-prometheus-stack) routes/notifies,
// taking over the role SNS played for CloudWatch.
func renderPrometheusRuleManifest(p MonitoringPlan) string {
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"monitoring.coreos.com/v1\"\n")
	b.WriteString("    kind       = \"PrometheusRule\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-alerts")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	// The `release` label lets the kube-prometheus-stack Prometheus select the rule
	// even with default selectors; we also disabled the nil-selector gate in CORE.
	b.WriteString("      labels    = { app = \"pyx-monitoring\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      groups = [\n")
	b.WriteString("        {\n")
	fmt.Fprintf(&b, "          name  = %q\n", p.Name+".alarms")
	b.WriteString("          rules = [\n")
	for _, a := range p.Alarms {
		writePrometheusAlertRule(&b, a)
	}
	b.WriteString("          ]\n")
	b.WriteString("        }\n")
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// writePrometheusAlertRule writes one alert rule mapped from a CloudWatch MetricAlarm.
func writePrometheusAlertRule(b *strings.Builder, a MetricAlarm) {
	expr := promExpr(a)
	forDur := promForDuration(a)
	severity := a.Severity
	if severity == "" {
		severity = "warning"
	}
	b.WriteString("            {\n")
	fmt.Fprintf(b, "              alert = %q\n", a.Name)
	fmt.Fprintf(b, "              expr  = %q\n", expr)
	fmt.Fprintf(b, "              for   = %q\n", forDur)
	b.WriteString("              labels = {\n")
	fmt.Fprintf(b, "                severity = %q\n", severity)
	b.WriteString("              }\n")
	b.WriteString("              annotations = {\n")
	fmt.Fprintf(b, "                summary = %q\n", a.Name+" threshold breached")
	if a.Namespace != "" {
		fmt.Fprintf(b, "                source  = %q\n", "migrated from CloudWatch "+a.Namespace+"/"+a.MetricName)
	}
	b.WriteString("              }\n")
	b.WriteString("            },\n")
}

// promExpr builds the PromQL expression for a CloudWatch MetricAlarm. An explicit
// PromQL override wins; otherwise the metric_name is used as the series and the
// comparison operator + threshold form the boolean expression.
func promExpr(a MetricAlarm) string {
	if strings.TrimSpace(a.PromQL) != "" {
		return a.PromQL
	}
	metric := a.MetricName
	if metric == "" {
		metric = "up"
	}
	return fmt.Sprintf("%s %s %g", metric, promOperator(a.ComparisonOperator), a.Threshold)
}

// promOperator maps a CloudWatch comparison_operator to a PromQL comparison operator.
func promOperator(cw string) string {
	switch cw {
	case "GreaterThanThreshold":
		return ">"
	case "GreaterThanOrEqualToThreshold":
		return ">="
	case "LessThanThreshold":
		return "<"
	case "LessThanOrEqualToThreshold":
		return "<="
	default:
		// Sane, explicit default rather than a silent wrong operator.
		return ">"
	}
}

// promForDuration maps evaluation_periods * period to a Prometheus `for` duration.
// Defaults mirror the CloudWatch render (1 period, 300s) so the alert is not flappy.
func promForDuration(a MetricAlarm) string {
	ep := a.EvaluationPeriods
	if ep <= 0 {
		ep = 1
	}
	per := a.PeriodSeconds
	if per <= 0 {
		per = 300
	}
	return fmt.Sprintf("%ds", ep*per)
}

// renderLokiDatasourceManifest renders a GrafanaDatasource CR pointing Grafana at
// the in-cluster Loki gateway — the CloudWatch Logs Insights replacement.
func renderLokiDatasourceManifest(p MonitoringPlan, lokiTFName string) string {
	lokiURL := fmt.Sprintf("http://%s-loki-gateway.%s.svc.cluster.local", p.Name, p.Namespace)
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"grafana.integreatly.org/v1beta1\"\n")
	b.WriteString("    kind       = \"GrafanaDatasource\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-loki")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      instanceSelector = { matchLabels = { dashboards = \"grafana\" } }\n")
	b.WriteString("      datasource = {\n")
	b.WriteString("        name   = \"Loki\"\n")
	b.WriteString("        type   = \"loki\"\n")
	b.WriteString("        access = \"proxy\"\n")
	fmt.Fprintf(&b, "        url    = %q\n", lokiURL)
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// renderTempoDatasourceManifest renders a GrafanaDatasource CR pointing Grafana at
// the tracing component's TempoStack query-frontend (operator reuse — the X-Ray
// replacement from the tracing component), so Grafana spans logs, metrics and traces.
func renderTempoDatasourceManifest(p MonitoringPlan) string {
	// The tempo-operator exposes the TempoStack query-frontend service:
	// tempo-<name>-tempo-query-frontend.<ns>.svc.cluster.local:3200.
	tempoURL := fmt.Sprintf("http://tempo-%s-tempo-query-frontend.%s.svc.cluster.local:%d",
		p.TempoDatasourceName, p.Namespace, defaultTempoQueryPort)
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"grafana.integreatly.org/v1beta1\"\n")
	b.WriteString("    kind       = \"GrafanaDatasource\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-tempo")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      instanceSelector = { matchLabels = { dashboards = \"grafana\" } }\n")
	b.WriteString("      datasource = {\n")
	b.WriteString("        name   = \"Tempo\"\n")
	b.WriteString("        type   = \"tempo\"\n")
	b.WriteString("        access = \"proxy\"\n")
	fmt.Fprintf(&b, "        url    = %q\n", tempoURL)
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// sortedKeys returns the map keys in deterministic order (rendered plans must be stable).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
