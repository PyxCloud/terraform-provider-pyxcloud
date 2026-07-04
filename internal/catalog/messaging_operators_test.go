package catalog

import (
	"strings"
	"testing"
)

// messaging_operators_test.go — tests for B1 (pd-MIG-B1-QUEUE-STREAM-OPERATORS):
// RabbitMQ Cluster Operator (managed-queue on DO) and Strimzi Kafka Operator
// (event-streaming on DO). Mirrors the patterns in macro_test.go and tracing_test.go.

// ── managed-queue / RabbitMQ Cluster Operator ────────────────────────────────

// TestTranslateQueueDORequiresClusterName verifies that DO returns a hard error
// when cluster_name is absent (never a silent single-VM fallback).
func TestTranslateQueueDORequiresClusterName(t *testing.T) {
	_, err := TranslateQueue(ctx(), MustEmbedded(), QueueSpec{
		Name: "jobs", Region: "Frankfurt", Provider: "digitalocean",
		// ClusterName intentionally omitted.
	})
	if err == nil {
		t.Fatal("expected error for DO queue without cluster_name, got nil")
	}
	if !strings.Contains(err.Error(), "cluster_name") {
		t.Errorf("expected error to mention cluster_name, got: %v", err)
	}
}

// TestTranslateQueueDOOperatorPlan verifies that a DO queue with a cluster_name
// resolves to the operator-pattern plan (RendersHelm, ResourceType=kubernetes_manifest).
func TestTranslateQueueDOOperatorPlan(t *testing.T) {
	plan, err := TranslateQueue(ctx(), MustEmbedded(), QueueSpec{
		Name:        "jobs",
		Region:      "Frankfurt",
		Provider:    "digitalocean",
		ClusterName: "prod-doks",
		FIFO:        true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Provider != ProviderDigitalOcean {
		t.Errorf("provider = %q, want %q", plan.Provider, ProviderDigitalOcean)
	}
	if plan.ResourceType != "kubernetes_manifest" {
		t.Errorf("ResourceType = %q, want kubernetes_manifest", plan.ResourceType)
	}
	if !plan.RendersHelm {
		t.Error("RendersHelm should be true for DO operator-pattern queue")
	}
	if plan.ClusterName != "prod-doks" {
		t.Errorf("ClusterName = %q, want prod-doks", plan.ClusterName)
	}
	if plan.Namespace != defaultRabbitMQNS {
		t.Errorf("Namespace = %q, want %q", plan.Namespace, defaultRabbitMQNS)
	}
	if plan.Replicas != defaultRabbitMQReplicas {
		t.Errorf("Replicas = %d, want %d", plan.Replicas, defaultRabbitMQReplicas)
	}
	if plan.Kind != KindQueue {
		t.Errorf("Kind = %q, want %q", plan.Kind, KindQueue)
	}
}

// TestRenderQueueDOOperatorHCL verifies the rendered HCL contains the
// RabbitMQ Cluster Operator CORE (helm_release) and EXTRA (RabbitmqCluster CR).
func TestRenderQueueDOOperatorHCL(t *testing.T) {
	plan, err := TranslateQueue(ctx(), MustEmbedded(), QueueSpec{
		Name:        "jobs",
		Region:      "Frankfurt",
		Provider:    "digitalocean",
		ClusterName: "prod-doks",
		FIFO:        true,
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	hcl, err := RenderMessagingHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	wantStrings := []string{
		// CORE: RabbitMQ Cluster Operator helm_release.
		`resource "helm_release"`,
		rabbitmqOperatorChart,
		rabbitmqOperatorVersion,
		`namespace  = "rabbitmq-system"`,
		// EXTRA: RabbitmqCluster CR.
		`resource "kubernetes_manifest"`,
		`apiVersion = "rabbitmq.com/v1beta1"`,
		`kind       = "RabbitmqCluster"`,
		// DOKS cluster data source.
		`data "digitalocean_kubernetes_cluster"`,
		`name = "prod-doks"`,
		// FIFO → quorum queue config.
		`default_queue_type = quorum`,
		// TLS.
		`disableNonTLSListeners = true`,
		// pyxcloud tag.
		`pyxcloud = "true"`,
	}
	for _, want := range wantStrings {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO queue HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestRenderQueueDONonFIFONoQuorumConfig verifies that a non-FIFO queue does
// not inject the quorum queue additionalConfig (it's only needed for ordering).
func TestRenderQueueDONonFIFONoQuorumConfig(t *testing.T) {
	plan, _ := TranslateQueue(ctx(), MustEmbedded(), QueueSpec{
		Name:        "tasks",
		Region:      "Frankfurt",
		Provider:    "digitalocean",
		ClusterName: "dev-doks",
	})
	hcl, _ := RenderMessagingHCL(plan)
	if strings.Contains(hcl, "default_queue_type") {
		t.Errorf("non-FIFO queue should not set default_queue_type:\n%s", hcl)
	}
}

// TestQueueDOCustomReplicasAndNamespace verifies that custom replicas/namespace
// override the defaults.
func TestQueueDOCustomReplicasAndNamespace(t *testing.T) {
	plan, err := TranslateQueue(ctx(), MustEmbedded(), QueueSpec{
		Name:        "jobs",
		Region:      "Frankfurt",
		Provider:    "digitalocean",
		ClusterName: "prod-doks",
		Namespace:   "messaging",
		Replicas:    5,
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if plan.Namespace != "messaging" {
		t.Errorf("Namespace = %q, want messaging", plan.Namespace)
	}
	if plan.Replicas != 5 {
		t.Errorf("Replicas = %d, want 5", plan.Replicas)
	}
	hcl, _ := RenderMessagingHCL(plan)
	if !strings.Contains(hcl, `namespace  = "messaging"`) {
		t.Errorf("HCL should contain custom namespace:\n%s", hcl)
	}
	if !strings.Contains(hcl, "replicas = 5") {
		t.Errorf("HCL should contain replicas = 5:\n%s", hcl)
	}
}

// ── event-streaming / Strimzi Kafka Operator ─────────────────────────────────

// TestTranslateStreamDORequiresClusterName verifies that DO returns a hard error
// when cluster_name is absent (never a silent single-VM fallback).
func TestTranslateStreamDORequiresClusterName(t *testing.T) {
	_, err := TranslateStream(ctx(), MustEmbedded(), StreamSpec{
		Name: "events", Region: "Frankfurt", Provider: "digitalocean",
		// ClusterName intentionally omitted.
	})
	if err == nil {
		t.Fatal("expected error for DO stream without cluster_name, got nil")
	}
	if !strings.Contains(err.Error(), "cluster_name") {
		t.Errorf("expected error to mention cluster_name, got: %v", err)
	}
}

// TestTranslateStreamDOOperatorPlan verifies that a DO stream with a cluster_name
// resolves to the operator-pattern plan (RendersHelm, ResourceType=kubernetes_manifest).
func TestTranslateStreamDOOperatorPlan(t *testing.T) {
	plan, err := TranslateStream(ctx(), MustEmbedded(), StreamSpec{
		Name:        "events",
		Region:      "Frankfurt",
		Provider:    "digitalocean",
		ClusterName: "prod-doks",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.Provider != ProviderDigitalOcean {
		t.Errorf("provider = %q, want %q", plan.Provider, ProviderDigitalOcean)
	}
	if plan.ResourceType != "kubernetes_manifest" {
		t.Errorf("ResourceType = %q, want kubernetes_manifest", plan.ResourceType)
	}
	if !plan.RendersHelm {
		t.Error("RendersHelm should be true for DO operator-pattern stream")
	}
	if plan.ClusterName != "prod-doks" {
		t.Errorf("ClusterName = %q, want prod-doks", plan.ClusterName)
	}
	if plan.Namespace != defaultKafkaNS {
		t.Errorf("Namespace = %q, want %q", plan.Namespace, defaultKafkaNS)
	}
	if plan.Replicas != defaultKafkaReplicas {
		t.Errorf("Replicas = %d, want %d", plan.Replicas, defaultKafkaReplicas)
	}
	if plan.RetentionHours != defaultKafkaRetentionHours {
		t.Errorf("RetentionHours = %d, want %d", plan.RetentionHours, defaultKafkaRetentionHours)
	}
	if plan.Kind != KindStream {
		t.Errorf("Kind = %q, want %q", plan.Kind, KindStream)
	}
}

// TestRenderStreamDOOperatorHCL verifies the rendered HCL contains the
// Strimzi Kafka Operator CORE (helm_release) and EXTRA (Kafka CR, KRaft mode).
func TestRenderStreamDOOperatorHCL(t *testing.T) {
	plan, err := TranslateStream(ctx(), MustEmbedded(), StreamSpec{
		Name:        "events",
		Region:      "Frankfurt",
		Provider:    "digitalocean",
		ClusterName: "prod-doks",
		Shards:      4,
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	hcl, err := RenderMessagingHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	wantStrings := []string{
		// CORE: Strimzi operator helm_release.
		`resource "helm_release"`,
		strimziOperatorChart,
		strimziOperatorVersion,
		`namespace  = "kafka"`,
		// EXTRA: Kafka CR.
		`resource "kubernetes_manifest"`,
		`apiVersion = "kafka.strimzi.io/v1beta2"`,
		`kind       = "Kafka"`,
		// KRaft mode annotations.
		`strimzi.io/kraft`,
		// TLS only (secure by default).
		`tls  = true`,
		// DOKS cluster data source.
		`data "digitalocean_kubernetes_cluster"`,
		`name = "prod-doks"`,
		// pyxcloud tag.
		`pyxcloud = "true"`,
	}
	for _, want := range wantStrings {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO stream HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestStreamDORetentionHoursDefault verifies that zero RetentionHours gets the
// default value in the plan (not zero/empty).
func TestStreamDORetentionHoursDefault(t *testing.T) {
	plan, _ := TranslateStream(ctx(), MustEmbedded(), StreamSpec{
		Name:        "events",
		Region:      "Frankfurt",
		Provider:    "digitalocean",
		ClusterName: "prod-doks",
		// RetentionHours omitted → default.
	})
	if plan.RetentionHours != defaultKafkaRetentionHours {
		t.Errorf("RetentionHours = %d, want default %d", plan.RetentionHours, defaultKafkaRetentionHours)
	}
}

// TestStreamDOCustomRetentionAndReplicas verifies custom fields override defaults.
func TestStreamDOCustomRetentionAndReplicas(t *testing.T) {
	plan, err := TranslateStream(ctx(), MustEmbedded(), StreamSpec{
		Name:           "events",
		Region:         "Frankfurt",
		Provider:       "digitalocean",
		ClusterName:    "prod-doks",
		Namespace:      "streaming",
		Replicas:       5,
		RetentionHours: 720,
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if plan.Replicas != 5 {
		t.Errorf("Replicas = %d, want 5", plan.Replicas)
	}
	if plan.RetentionHours != 720 {
		t.Errorf("RetentionHours = %d, want 720", plan.RetentionHours)
	}
	hcl, _ := RenderMessagingHCL(plan)
	if !strings.Contains(hcl, `namespace  = "streaming"`) {
		t.Errorf("HCL should contain custom namespace:\n%s", hcl)
	}
	if !strings.Contains(hcl, "720") {
		t.Errorf("HCL should contain retention 720:\n%s", hcl)
	}
}

// ── mitigation bypass: DO queue/stream no longer hits single-VM path ─────────

// TestQueueStreamDONativelySupported verifies that NativelySupported returns true
// for DO on queue/stream types (so assemble.go does NOT take the mitigation path).
func TestQueueStreamDONativelySupported(t *testing.T) {
	types := []string{"managed-queue", "message-queue", "event-streaming", "event-bus"}
	for _, typ := range types {
		if !NativelySupported(typ, ProviderDigitalOcean) {
			t.Errorf("NativelySupported(%q, DO) = false, want true (operator-pattern)", typ)
		}
	}
}

// TestHasOperatorAlternativeQueueStream verifies that queue/stream types are
// recognised as having an operator alternative (for architecture detection).
func TestHasOperatorAlternativeQueueStream(t *testing.T) {
	operatorTypes := []string{
		"managed-queue", "message-queue", "event-streaming", "event-bus",
	}
	for _, typ := range operatorTypes {
		if !HasOperatorAlternative(typ) {
			t.Errorf("HasOperatorAlternative(%q) = false, want true", typ)
		}
	}
}

// TestAssembleHCLDOQueueOperatorPinsHelm verifies that AssembleHCL pins the
// helm + kubernetes providers when a DO queue component is assembled.
func TestAssembleHCLDOQueueOperatorPinsHelm(t *testing.T) {
	in := AssembleInput{
		Name:     "test-env",
		Provider: "digitalocean",
		Region:   "Frankfurt",
		Components: []AssembleComponent{
			{
				Name: "jobs",
				Type: "managed-queue",
				Queue: &AssembleQueue{
					ClusterName: "prod-doks",
				},
			},
		},
	}
	docs, err := AssembleHCL(ctx(), MustEmbedded(), in)
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	all := strings.Join(docs, "\n")
	// Should pin hashicorp/helm + hashicorp/kubernetes providers.
	if !strings.Contains(all, `source = "hashicorp/helm"`) {
		t.Errorf("expected helm provider pin in assembled output:\n%s", all)
	}
	if !strings.Contains(all, `source = "hashicorp/kubernetes"`) {
		t.Errorf("expected kubernetes provider pin in assembled output:\n%s", all)
	}
	// Should emit the RabbitMQ operator chart.
	if !strings.Contains(all, rabbitmqOperatorChart) {
		t.Errorf("expected rabbitmq-cluster-operator chart in assembled output:\n%s", all)
	}
}

// TestAssembleHCLDOStreamOperatorPinsHelm verifies that AssembleHCL pins the
// helm + kubernetes providers when a DO stream component is assembled.
func TestAssembleHCLDOStreamOperatorPinsHelm(t *testing.T) {
	in := AssembleInput{
		Name:     "test-env",
		Provider: "digitalocean",
		Region:   "Frankfurt",
		Components: []AssembleComponent{
			{
				Name: "events",
				Type: "event-streaming",
				Stream: &AssembleStream{
					ClusterName: "prod-doks",
				},
			},
		},
	}
	docs, err := AssembleHCL(ctx(), MustEmbedded(), in)
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, `source = "hashicorp/helm"`) {
		t.Errorf("expected helm provider pin in assembled output:\n%s", all)
	}
	if !strings.Contains(all, `source = "hashicorp/kubernetes"`) {
		t.Errorf("expected kubernetes provider pin in assembled output:\n%s", all)
	}
	if !strings.Contains(all, strimziOperatorChart) {
		t.Errorf("expected strimzi-kafka-operator chart in assembled output:\n%s", all)
	}
}
