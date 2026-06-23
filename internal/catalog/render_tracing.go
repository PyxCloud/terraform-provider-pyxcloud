package catalog

import (
	"fmt"
	"strings"
)

// RenderTracingHCL renders a TracingPlan into provider HCL.
// AWS -> aws_xray_group + aws_xray_sampling_rule (the X-Ray service being
// migrated away from); DigitalOcean -> the OPERATOR pattern: the OpenTelemetry
// Operator + the Tempo Operator as upstream Helm releases (CORE) plus an
// OpenTelemetryCollector + a TempoStack custom resource (EXTRA). Any other
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

// renderTracingDO renders the DigitalOcean tracing replacement following the
// Kubernetes OPERATOR pattern (pd-MIG-OPERATOR-PATTERN-CONVENTION):
//
//   - CORE (upstream, via the operator-pattern helper): the OpenTelemetry Operator
//     and the Tempo Operator, each installed as a `helm_release` of its official
//     chart. The charts own the controllers, RBAC and CRDs — we do NOT hand-roll
//     the Tempo / collector Deployments + Services any more.
//   - EXTRA (ours): a `TempoStack` custom resource (the operator provisions Tempo's
//     micro-services + storage) and an `OpenTelemetryCollector` custom resource
//     (the operator provisions the collector Deployment + Service that ingests OTLP
//     and exports to the TempoStack). These are the X-Ray-daemon replacements.
//
// All resources land on the existing DOKS cluster via the shared
// `data "digitalocean_kubernetes_cluster"` reference.
func renderTracingDO(p TracingPlan) string {
	name := tfName(p.Name)
	clusterData := name + "_cluster"
	otelOp := name + "_otel_operator"
	tempoOp := name + "_tempo_operator"
	tempoStack := name + "_tempostack"
	collector := name + "_collector"

	// ── CORE: upstream operators (controllers + CRDs) via their Helm charts ──
	core := []HelmReleaseSpec{
		{
			TFName:          otelOp,
			ReleaseName:     p.Name + "-otel-operator",
			Repository:      otelOperatorRepo,
			Chart:           otelOperatorChart,
			Version:         otelOperatorVersion,
			Namespace:       p.Namespace,
			CreateNamespace: true,
			ClusterDataRef:  clusterData,
		},
		{
			TFName:          tempoOp,
			ReleaseName:     p.Name + "-tempo-operator",
			Repository:      tempoOperatorRepo,
			Chart:           tempoOperatorChart,
			Version:         tempoOperatorVersion,
			Namespace:       p.Namespace,
			CreateNamespace: true,
			ClusterDataRef:  clusterData,
		},
	}

	// ── EXTRA: our custom resources the operators reconcile ──
	tempoStackCR := ManifestCR{
		TFName:    tempoStack,
		Manifest:  renderTempoStackManifest(p),
		DependsOn: []string{"helm_release." + tempoOp},
	}
	collectorCR := ManifestCR{
		TFName:   collector,
		Manifest: renderOTelCollectorManifest(p),
		// The collector exports to the TempoStack's gateway, so it must wait for both
		// the OTel operator (owns its CRD) and the TempoStack (its export target).
		DependsOn: []string{"helm_release." + otelOp, "kubernetes_manifest." + tempoStack},
	}

	return renderOperatorComponent(clusterData, p.ClusterName, core, []ManifestCR{tempoStackCR, collectorCR})
}

// renderTempoStackManifest renders the EXTRA `TempoStack` CR body (the
// tempo-operator API). The operator provisions Tempo's components + storage;
// global block retention is bounded (never unbounded).
func renderTempoStackManifest(p TracingPlan) string {
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"tempo.grafana.com/v1alpha1\"\n")
	b.WriteString("    kind       = \"TempoStack\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-tempo")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"tempo\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	// Storage secret holds the object-storage credentials/endpoint the operator
	// uses for trace blocks (supplied out-of-band, never in state).
	b.WriteString("      storage = {\n")
	b.WriteString("        secret = {\n")
	fmt.Fprintf(&b, "          name = %q\n", p.Name+"-tempo-storage")
	b.WriteString("          type = \"s3\"\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	// storageSize bounds the per-ingester PVC; retention bounds block age.
	b.WriteString("      storageSize = \"10Gi\"\n")
	b.WriteString("      retention = {\n")
	b.WriteString("        global = {\n")
	fmt.Fprintf(&b, "          traces = \"%dh\"\n", p.RetentionHours)
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	// Enable the distributor's OTLP receivers (the collector exports here).
	b.WriteString("      template = {\n")
	b.WriteString("        distributor = {\n")
	b.WriteString("          component = {\n")
	b.WriteString("            replicas = 1\n")
	b.WriteString("          }\n")
	b.WriteString("        }\n")
	b.WriteString("        queryFrontend = {\n")
	b.WriteString("          component = {\n")
	b.WriteString("            replicas = 1\n")
	b.WriteString("          }\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// renderOTelCollectorManifest renders the EXTRA `OpenTelemetryCollector` CR body
// (the opentelemetry-operator API). The operator provisions the collector
// Deployment + Service from the embedded pipeline config: OTLP receivers ->
// probabilistic sampler -> OTLP exporter to the TempoStack distributor gateway.
func renderOTelCollectorManifest(p TracingPlan) string {
	// The tempo-operator exposes the TempoStack's OTLP ingest on the distributor
	// service: tempo-<name>-distributor.<ns>.svc.cluster.local:4317.
	tempoEndpoint := fmt.Sprintf("tempo-%s-tempo-distributor.%s.svc.cluster.local:%d", p.Name, p.Namespace, p.OTLPGRPCPort)
	samplingPct := p.SamplingRate * 100

	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"opentelemetry.io/v1beta1\"\n")
	b.WriteString("    kind       = \"OpenTelemetryCollector\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-otel-collector")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"otel-collector\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      mode     = \"deployment\"\n")
	fmt.Fprintf(&b, "      image    = %q\n", p.CollectorImage)
	b.WriteString("      replicas = 1\n")
	// The collector pipeline config — a structured object the operator renders into
	// the collector ConfigMap (the v1beta1 CR config is a typed map, not a YAML blob).
	b.WriteString("      config = {\n")
	b.WriteString("        receivers = {\n")
	b.WriteString("          otlp = {\n")
	b.WriteString("            protocols = {\n")
	fmt.Fprintf(&b, "              grpc = { endpoint = \"0.0.0.0:%d\" }\n", p.OTLPGRPCPort)
	fmt.Fprintf(&b, "              http = { endpoint = \"0.0.0.0:%d\" }\n", p.OTLPHTTPPort)
	b.WriteString("            }\n")
	b.WriteString("          }\n")
	b.WriteString("        }\n")
	b.WriteString("        processors = {\n")
	fmt.Fprintf(&b, "          probabilistic_sampler = { sampling_percentage = %g }\n", samplingPct)
	b.WriteString("          batch                 = {}\n")
	b.WriteString("        }\n")
	b.WriteString("        exporters = {\n")
	b.WriteString("          \"otlp/tempo\" = {\n")
	fmt.Fprintf(&b, "            endpoint = %q\n", tempoEndpoint)
	b.WriteString("            tls      = { insecure = true }\n")
	b.WriteString("          }\n")
	b.WriteString("        }\n")
	b.WriteString("        service = {\n")
	b.WriteString("          pipelines = {\n")
	b.WriteString("            traces = {\n")
	b.WriteString("              receivers  = [\"otlp\"]\n")
	b.WriteString("              processors = [\"probabilistic_sampler\", \"batch\"]\n")
	b.WriteString("              exporters  = [\"otlp/tempo\"]\n")
	b.WriteString("            }\n")
	b.WriteString("          }\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}
