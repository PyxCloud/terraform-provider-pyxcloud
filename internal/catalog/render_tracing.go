package catalog

import (
	"fmt"
	"strings"
)

// RenderTracingHCL renders a TracingPlan into provider HCL.
// AWS -> aws_xray_group + aws_xray_sampling_rule (the X-Ray service being
// migrated away from); DigitalOcean -> Grafana Tempo + an OpenTelemetry collector
// on DOKS (kubernetes_manifest Deployments/Services/ConfigMap). Any other
// provider never reaches here (TranslateTracing rejects it with a clean
// ErrComponentUnsupported).
func RenderTracingHCL(plan TracingPlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderTracingAWS(plan), nil
	case ProviderDigitalOcean:
		return renderTracingDO(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q for tracing", plan.Provider)
	}
}

func renderTracingAWS(p TracingPlan) string {
	name := tfName(p.Name)
	var b strings.Builder

	// X-Ray group: a saved filter expression traces flow into. The ALL filter is
	// the catch-all group; a real environment can narrow it later.
	fmt.Fprintf(&b, "resource \"aws_xray_group\" %q {\n", name)
	fmt.Fprintf(&b, "  group_name        = %q\n", p.Name)
	b.WriteString("  filter_expression = \"service(\\\"*\\\")\"\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n\n")

	// Sampling rule: bounds trace volume/cost. fixed_rate is the resolved sampling
	// rate; reservoir_size guarantees a minimum trace/sec floor.
	fmt.Fprintf(&b, "resource \"aws_xray_sampling_rule\" %q {\n", name+"_sampling")
	fmt.Fprintf(&b, "  rule_name      = %q\n", p.Name+"-sampling")
	b.WriteString("  priority       = 1000\n")
	b.WriteString("  version        = 1\n")
	fmt.Fprintf(&b, "  fixed_rate     = %g\n", p.SamplingRate)
	b.WriteString("  reservoir_size = 1\n")
	b.WriteString("  host           = \"*\"\n")
	b.WriteString("  http_method    = \"*\"\n")
	b.WriteString("  service_name   = \"*\"\n")
	b.WriteString("  service_type   = \"*\"\n")
	b.WriteString("  resource_arn   = \"*\"\n")
	b.WriteString("  url_path       = \"*\"\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n")
	return b.String()
}

func renderTracingDO(p TracingPlan) string {
	name := tfName(p.Name)
	tempo := name + "_tempo"
	coll := name + "_collector"
	var b strings.Builder

	// The pipeline runs IN the DOKS cluster; reference it via a data source so the
	// manifests land on the right cluster's kube credentials (the same convention
	// the cert-manager / scheduled-trigger DOKS paths use).
	fmt.Fprintf(&b, "data \"digitalocean_kubernetes_cluster\" %q {\n", name+"_cluster")
	fmt.Fprintf(&b, "  name = %q\n", p.ClusterName)
	b.WriteString("}\n\n")

	// ── Grafana Tempo: a Deployment (trace store) fronted by a Service exposing the
	// OTLP ingest ports + the query port. retention bounds the block retention.
	fmt.Fprintf(&b, "resource \"kubernetes_manifest\" %q {\n", tempo+"_deployment")
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"apps/v1\"\n")
	b.WriteString("    kind       = \"Deployment\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-tempo")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"tempo\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      replicas = 1\n")
	b.WriteString("      selector = { matchLabels = { app = \"tempo\" } }\n")
	b.WriteString("      template = {\n")
	b.WriteString("        metadata = { labels = { app = \"tempo\" } }\n")
	b.WriteString("        spec = {\n")
	b.WriteString("          containers = [{\n")
	b.WriteString("            name  = \"tempo\"\n")
	fmt.Fprintf(&b, "            image = %q\n", p.TempoImage)
	fmt.Fprintf(&b, "            args  = [\"-config.file=/etc/tempo/tempo.yaml\", \"-storage.trace.local.path=/var/tempo\", \"-config.expand-env=true\"]\n")
	b.WriteString("            ports = [\n")
	fmt.Fprintf(&b, "              { containerPort = %d, name = \"otlp-grpc\" },\n", p.OTLPGRPCPort)
	fmt.Fprintf(&b, "              { containerPort = %d, name = \"http-query\" },\n", p.TempoQueryPort)
	b.WriteString("            ]\n")
	fmt.Fprintf(&b, "            env = [{ name = \"TEMPO_RETENTION\", value = \"%dh\" }]\n", p.RetentionHours)
	b.WriteString("          }]\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	// Tempo Service: the stable in-cluster endpoint the collector exports to and
	// Grafana queries.
	fmt.Fprintf(&b, "resource \"kubernetes_manifest\" %q {\n", tempo+"_service")
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"v1\"\n")
	b.WriteString("    kind       = \"Service\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-tempo")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      selector = { app = \"tempo\" }\n")
	b.WriteString("      ports = [\n")
	fmt.Fprintf(&b, "        { name = \"otlp-grpc\", port = %d, targetPort = %d },\n", p.OTLPGRPCPort, p.OTLPGRPCPort)
	fmt.Fprintf(&b, "        { name = \"http-query\", port = %d, targetPort = %d },\n", p.TempoQueryPort, p.TempoQueryPort)
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  depends_on = [kubernetes_manifest.%s]\n", tempo+"_deployment")
	b.WriteString("}\n\n")

	// ── OpenTelemetry collector: a ConfigMap (the pipeline config: OTLP receivers ->
	// probabilistic sampler -> OTLP exporter to Tempo) + a Deployment + a Service
	// exposing the OTLP receivers applications send spans to.
	tempoEndpoint := fmt.Sprintf("%s-tempo.%s.svc.cluster.local:%d", p.Name, p.Namespace, p.OTLPGRPCPort)
	samplingPct := p.SamplingRate * 100
	collectorConfig := strings.Join([]string{
		"receivers:",
		"  otlp:",
		"    protocols:",
		"      grpc:",
		fmt.Sprintf("        endpoint: 0.0.0.0:%d", p.OTLPGRPCPort),
		"      http:",
		fmt.Sprintf("        endpoint: 0.0.0.0:%d", p.OTLPHTTPPort),
		"processors:",
		"  probabilistic_sampler:",
		fmt.Sprintf("    sampling_percentage: %g", samplingPct),
		"  batch: {}",
		"exporters:",
		"  otlp/tempo:",
		fmt.Sprintf("    endpoint: %s", tempoEndpoint),
		"    tls:",
		"      insecure: true",
		"service:",
		"  pipelines:",
		"    traces:",
		"      receivers: [otlp]",
		"      processors: [probabilistic_sampler, batch]",
		"      exporters: [otlp/tempo]",
		"",
	}, "\n")

	fmt.Fprintf(&b, "resource \"kubernetes_manifest\" %q {\n", coll+"_config")
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"v1\"\n")
	b.WriteString("    kind       = \"ConfigMap\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-otel-collector")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("    }\n")
	b.WriteString("    data = {\n")
	fmt.Fprintf(&b, "      \"collector.yaml\" = %s\n", hclHeredoc("OTELCONFIG", collectorConfig))
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	b.WriteString("}\n\n")

	fmt.Fprintf(&b, "resource \"kubernetes_manifest\" %q {\n", coll+"_deployment")
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"apps/v1\"\n")
	b.WriteString("    kind       = \"Deployment\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-otel-collector")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"otel-collector\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      replicas = 1\n")
	b.WriteString("      selector = { matchLabels = { app = \"otel-collector\" } }\n")
	b.WriteString("      template = {\n")
	b.WriteString("        metadata = { labels = { app = \"otel-collector\" } }\n")
	b.WriteString("        spec = {\n")
	b.WriteString("          containers = [{\n")
	b.WriteString("            name  = \"otel-collector\"\n")
	fmt.Fprintf(&b, "            image = %q\n", p.CollectorImage)
	b.WriteString("            args  = [\"--config=/conf/collector.yaml\"]\n")
	b.WriteString("            ports = [\n")
	fmt.Fprintf(&b, "              { containerPort = %d, name = \"otlp-grpc\" },\n", p.OTLPGRPCPort)
	fmt.Fprintf(&b, "              { containerPort = %d, name = \"otlp-http\" },\n", p.OTLPHTTPPort)
	b.WriteString("            ]\n")
	b.WriteString("            volumeMounts = [{ name = \"config\", mountPath = \"/conf\" }]\n")
	b.WriteString("          }]\n")
	fmt.Fprintf(&b, "          volumes = [{ name = \"config\", configMap = { name = %q } }]\n", p.Name+"-otel-collector")
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  depends_on = [kubernetes_manifest.%s, kubernetes_manifest.%s]\n", coll+"_config", tempo+"_service")
	b.WriteString("}\n\n")

	// Collector Service: the OTLP ingest endpoint applications point their tracer at
	// (the X-Ray-daemon replacement). Same OTLP grpc/http ports.
	fmt.Fprintf(&b, "resource \"kubernetes_manifest\" %q {\n", coll+"_service")
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"v1\"\n")
	b.WriteString("    kind       = \"Service\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-otel-collector")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      selector = { app = \"otel-collector\" }\n")
	b.WriteString("      ports = [\n")
	fmt.Fprintf(&b, "        { name = \"otlp-grpc\", port = %d, targetPort = %d },\n", p.OTLPGRPCPort, p.OTLPGRPCPort)
	fmt.Fprintf(&b, "        { name = \"otlp-http\", port = %d, targetPort = %d },\n", p.OTLPHTTPPort, p.OTLPHTTPPort)
	b.WriteString("      ]\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	fmt.Fprintf(&b, "  depends_on = [kubernetes_manifest.%s]\n", coll+"_deployment")
	b.WriteString("}\n")
	return b.String()
}

// hclHeredoc renders a multi-line string as an HCL heredoc with the given tag so
// embedded YAML stays readable and quote-safe in the generated .tf.
func hclHeredoc(tag, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<<-%s\n", tag)
	b.WriteString(body)
	fmt.Fprintf(&b, "%s", tag)
	return b.String()
}
