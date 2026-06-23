package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTranslateTracingAWS asserts the X-Ray plan: catalog-resolved region,
// default sampling, aws_xray_group type.
func TestTranslateTracingAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTracing(context.Background(), MustEmbedded(), TracingSpec{
		Name: "app-traces", Region: "Frankfurt", Provider: "aws",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_xray_group" {
		t.Errorf("resource_type = %q, want aws_xray_group", plan.ResourceType)
	}
	if plan.SamplingRate != 0.1 {
		t.Errorf("default sampling = %v, want 0.1", plan.SamplingRate)
	}
}

// TestTranslateTracingDO asserts the Tempo + OTel-collector plan on DOKS:
// defaulted namespace/images/retention, kubernetes_manifest type.
func TestTranslateTracingDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTracing(context.Background(), MustEmbedded(), TracingSpec{
		Name: "app-traces", Region: "Frankfurt", Provider: "digitalocean",
		ClusterName: "prod-doks",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "kubernetes_manifest" {
		t.Errorf("resource_type = %q, want kubernetes_manifest", plan.ResourceType)
	}
	if plan.Namespace != defaultTracingNS {
		t.Errorf("namespace = %q, want %q", plan.Namespace, defaultTracingNS)
	}
	if plan.TempoImage != defaultTempoImage || plan.CollectorImage != defaultOTelCollImage {
		t.Errorf("images = %q / %q, want defaults", plan.TempoImage, plan.CollectorImage)
	}
	if plan.RetentionHours != 72 {
		t.Errorf("retention = %d, want 72", plan.RetentionHours)
	}
	if plan.OTLPGRPCPort != defaultOTLPGRPCPort {
		t.Errorf("otlp grpc port = %d, want %d", plan.OTLPGRPCPort, defaultOTLPGRPCPort)
	}
}

// TestTranslateTracingDORequiresCluster asserts the DO path's hard plan-time
// error (no silent fallback) for the missing cluster case.
func TestTranslateTracingDORequiresCluster(t *testing.T) {
	t.Parallel()
	_, err := TranslateTracing(context.Background(), MustEmbedded(), TracingSpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean",
	})
	if err == nil || !strings.Contains(err.Error(), "cluster_name is required") {
		t.Fatalf("missing cluster must be a hard error, got %v", err)
	}
}

// TestTranslateTracingSamplingOverride asserts a custom sampling rate flows
// through and is range-validated.
func TestTranslateTracingSamplingOverride(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTracing(context.Background(), MustEmbedded(), TracingSpec{
		Name: "x", Region: "Frankfurt", Provider: "aws", SamplingRate: 0.25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SamplingRate != 0.25 {
		t.Errorf("sampling = %v, want 0.25", plan.SamplingRate)
	}
	// Out-of-range sampling is rejected.
	if _, err := TranslateTracing(context.Background(), MustEmbedded(), TracingSpec{
		Name: "x", Region: "Frankfurt", Provider: "aws", SamplingRate: 1.5,
	}); err == nil || !strings.Contains(err.Error(), "sampling_rate") {
		t.Fatalf("out-of-range sampling must be a hard error, got %v", err)
	}
}

// TestTranslateTracingRegionNotFound asserts an unresolvable region errors.
func TestTranslateTracingRegionNotFound(t *testing.T) {
	t.Parallel()
	_, err := TranslateTracing(context.Background(), MustEmbedded(), TracingSpec{
		Name: "x", Region: "Atlantis", Provider: "aws",
	})
	var nf ErrRegionNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrRegionNotFound, got %T: %v", err, err)
	}
}

// TestTranslateTracingUnsupportedProvider asserts an unsupported provider
// surfaces ErrComponentUnsupported.
func TestTranslateTracingUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := TranslateTracing(context.Background(), MustEmbedded(), TracingSpec{
		Name: "x", Region: "Frankfurt", Provider: "gcp",
	})
	var un ErrComponentUnsupported
	if !errors.As(err, &un) {
		t.Fatalf("expected ErrComponentUnsupported, got %T: %v", err, err)
	}
}

func TestTracingValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []TracingSpec{
		{Provider: "aws"},                                          // missing region
		{Region: "Frankfurt"},                                      // missing provider
		{Region: "Frankfurt", Provider: "vultr"},                   // unknown provider
		{Region: "Frankfurt", Provider: "aws", RetentionHours: -1}, // bad retention
	}
	for i, c := range cases {
		if _, err := TranslateTracing(context.Background(), cat, c); err == nil {
			t.Errorf("case %d: expected validation error, got nil", i)
		}
	}
}

func TestCanonicalTracingType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"tracing", "distributed-tracing", "tempo", "trace-collector", "otel-tracing", " TEMPO "} {
		got, ok := CanonicalTracingType(in)
		if !ok || got != TypeTracing {
			t.Errorf("%q -> %q,%v want tracing,true", in, got, ok)
		}
	}
	if _, ok := CanonicalTracingType("virtual-machine"); ok {
		t.Error("virtual-machine is not a tracing type")
	}
}

// ── RENDER TESTS ─────────────────────────────────────────────────────────────

func TestRenderTracingAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTracing(context.Background(), MustEmbedded(), TracingSpec{
		Name: "app-traces", Region: "Frankfurt", Provider: "aws", SamplingRate: 0.2,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderTracingHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_xray_group" "app-traces"`,
		`group_name        = "app-traces"`,
		`resource "aws_xray_sampling_rule" "app-traces_sampling"`,
		`fixed_rate     = 0.2`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws X-Ray HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL not ASCII:\n%s", hcl)
	}
}

func TestRenderTracingDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateTracing(context.Background(), MustEmbedded(), TracingSpec{
		Name: "app-traces", Region: "Frankfurt", Provider: "digitalocean",
		ClusterName: "prod-doks", SamplingRate: 0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderTracingHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		// shared DOKS cluster data source
		`data "digitalocean_kubernetes_cluster" "app-traces_cluster"`,
		// CORE: upstream operators via helm_release (the operator-pattern CORE)
		`resource "helm_release" "app-traces_otel_operator"`,
		`chart      = "opentelemetry-operator"`,
		`resource "helm_release" "app-traces_tempo_operator"`,
		`chart      = "tempo-operator"`,
		// EXTRA: our custom resources the operators reconcile
		`kind       = "TempoStack"`,
		`kind       = "OpenTelemetryCollector"`,
		`image    = "` + defaultOTelCollImage + `"`,
		`probabilistic_sampler = { sampling_percentage = 50 }`,
		`endpoint = "tempo-app-traces-tempo-distributor.observability.svc.cluster.local:4317"`,
		`exporters  = ["otlp/tempo"]`,
		// EXTRA depends on CORE (operator owns the CRD)
		`depends_on = [helm_release.app-traces_otel_operator`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do operator-pattern tracing HCL missing %q:\n%s", want, hcl)
		}
	}
	// The hand-rolled raw Deployments/Services/ConfigMap are gone — the operators own them.
	for _, gone := range []string{`kind       = "ConfigMap"`, `kind       = "Deployment"`} {
		if strings.Contains(hcl, gone) {
			t.Errorf("operator pattern must not hand-roll %q:\n%s", gone, hcl)
		}
	}
}

func TestRenderTracingUnsupportedProvider(t *testing.T) {
	t.Parallel()
	if _, err := RenderTracingHCL(TracingPlan{Provider: "gcp"}); err == nil {
		t.Fatal("expected render error for unsupported provider, got nil")
	}
}
