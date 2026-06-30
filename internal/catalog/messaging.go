package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Messaging covers the two canonical messaging macro components (SPEC §5.8):
//
//   managed-queue / message-queue — a point-to-point work queue:
//     - AWS: aws_sqs_queue (+ a redrive dead-letter queue when retries are set).
//     - GCP: a google_pubsub_topic + google_pubsub_subscription (pull) pair — the
//       Pub/Sub analogue of a queue (a subscription is the durable backlog).
//     - DigitalOcean: OPERATOR PATTERN on DOKS (B1 — pd-MIG-B1-QUEUE-STREAM-OPERATORS).
//       DO has no managed queue/broker primitive, so SQS is replaced by the
//       RabbitMQ Cluster Operator (rabbitmq/cluster-operator Helm chart → CORE)
//       + a RabbitmqCluster CR (EXTRA). ClusterName is required on DO; without it
//       a clean plan-time error is returned rather than a silent fallback.
//
//   event-streaming / event-bus — an ordered, replayable, multi-consumer stream:
//     - AWS: aws_kinesis_stream (on-demand) — the streaming primitive.
//     - GCP: a google_pubsub_topic (Pub/Sub is GCP's stream + bus; consumers
//       attach their own subscriptions). One topic = the bus.
//     - DigitalOcean: OPERATOR PATTERN on DOKS (B1). DO has no managed streaming
//       primitive, so Kinesis is replaced by the Strimzi Kafka operator
//       (strimzi/strimzi-kafka-operator Helm chart → CORE) + a Kafka CR (EXTRA).
//       ClusterName required.
//
// SECURITY: queues/streams are private to the account/project by IAM; PyxCloud
// never attaches a public access policy. Encryption at rest is on by default
// (SQS SSE-SQS, Kinesis KMS, Pub/Sub is encrypted by Google-managed keys).

// MessagingKind selects which messaging component to translate.
type MessagingKind string

const (
	KindQueue  MessagingKind = "managed-queue"
	KindStream MessagingKind = "event-streaming"
)

// Operator-pattern defaults for the DO queue/stream operator components (B1).
const (
	// RabbitMQ Cluster Operator defaults.
	defaultRabbitMQNS      = "rabbitmq-system"
	defaultRabbitMQReplicas = 3 // HA: 3 nodes (quorum queue needs odd quorum)

	// Strimzi Kafka operator defaults.
	defaultKafkaNS            = "kafka"
	defaultKafkaReplicas      = 3 // HA: 3 brokers
	defaultKafkaRetentionHours = 168 // 7 days

	// Operator chart coordinates — pinned for deterministic plans.
	rabbitmqOperatorRepo    = "https://charts.bitnami.com/bitnami"
	rabbitmqOperatorChart   = "rabbitmq-cluster-operator"
	rabbitmqOperatorVersion = "4.3.27"

	strimziOperatorRepo    = "https://strimzi.io/charts/"
	strimziOperatorChart   = "strimzi-kafka-operator"
	strimziOperatorVersion = "0.42.0"
)

// QueueSpec is the abstract managed-queue description. Provider-neutral.
type QueueSpec struct {
	Name     string
	Region   string
	Provider string

	// FIFO requests strict ordering + exactly-once (SQS FIFO / Pub/Sub message
	// ordering). Defaults to a standard best-effort queue.
	FIFO bool
	// VisibilityTimeoutSeconds is how long a delivered message is hidden from other
	// consumers (SQS visibility timeout / Pub/Sub ack deadline). 0 -> provider default.
	VisibilityTimeoutSeconds int
	// MaxReceiveCount, when > 0, wires a dead-letter queue (SQS redrive). 0 -> none.
	MaxReceiveCount int

	// ── DigitalOcean operator-pattern fields (B1) ──
	// ClusterName is the existing DOKS cluster the RabbitMQ Cluster Operator runs on.
	// Required for DO; ignored on other providers.
	ClusterName string
	// Namespace is the Kubernetes namespace for the RabbitMQ operator + cluster.
	// Empty -> "rabbitmq-system".
	Namespace string
	// Replicas is the number of RabbitmqCluster replicas (HA). 0 -> 3 (default HA).
	Replicas int
}

// StreamSpec is the abstract event-streaming description. Provider-neutral.
type StreamSpec struct {
	Name     string
	Region   string
	Provider string

	// Shards is the Kinesis shard count when provisioned mode is desired; 0 selects
	// on-demand capacity (the simplest, cheapest-to-reason default). Pub/Sub has no
	// shard concept (it autoscales), so this is AWS-only intent.
	Shards int
	// RetentionHours is how long records are retained/replayable. 0 -> provider default.
	RetentionHours int

	// ── DigitalOcean operator-pattern fields (B1) ──
	// ClusterName is the existing DOKS cluster the Strimzi operator runs on.
	// Required for DO; ignored on other providers.
	ClusterName string
	// Namespace is the Kubernetes namespace for the Strimzi operator + Kafka cluster.
	// Empty -> "kafka".
	Namespace string
	// Replicas is the number of Kafka broker replicas. 0 -> 3 (default HA).
	Replicas int
}

// MessagingPlan is the deterministic, catalog-resolved concrete translation of a
// queue OR a stream (one struct, Kind selects the shape).
type MessagingPlan struct {
	Kind       MessagingKind `json:"kind"`
	Provider   string        `json:"provider"`
	CSP        string        `json:"csp"`
	RegionName string        `json:"region_name"`
	CSPRegion  string        `json:"csp_region"`

	Name string `json:"name"`

	// Queue fields.
	FIFO                     bool `json:"fifo,omitempty"`
	VisibilityTimeoutSeconds int  `json:"visibility_timeout_seconds,omitempty"`
	MaxReceiveCount          int  `json:"max_receive_count,omitempty"`

	// Stream fields.
	Shards         int `json:"shards,omitempty"`
	RetentionHours int `json:"retention_hours,omitempty"`

	// ── DigitalOcean operator-pattern fields (B1: pd-MIG-B1-QUEUE-STREAM-OPERATORS) ──
	// ClusterName is the existing DOKS cluster the operator runs on (DO only).
	ClusterName string `json:"cluster_name,omitempty"`
	// Namespace is the Kubernetes namespace for the operator + CR workloads (DO only).
	Namespace string `json:"namespace,omitempty"`
	// Replicas is the number of broker/cluster replicas for HA (DO only).
	Replicas int `json:"replicas,omitempty"`
	// RendersHelm is true when the render emits a helm_release (the operator-pattern
	// CORE) — assemble.go pins hashicorp/helm (needsHelm) when set.
	RendersHelm bool `json:"renders_helm,omitempty"`

	ResourceType string `json:"resource_type"`
}

// TranslateQueue resolves a QueueSpec into a concrete MessagingPlan. DO is a
// clean unsupported error (no managed queue primitive).
func TranslateQueue(ctx context.Context, cat RegionCatalog, spec QueueSpec) (MessagingPlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return MessagingPlan{}, fmt.Errorf("managed-queue: region (abstract pyx region_name) is required")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return MessagingPlan{}, fmt.Errorf("managed-queue: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if spec.VisibilityTimeoutSeconds < 0 {
		return MessagingPlan{}, fmt.Errorf("managed-queue: visibility_timeout_seconds must be >= 0")
	}
	if spec.MaxReceiveCount < 0 {
		return MessagingPlan{}, fmt.Errorf("managed-queue: max_receive_count must be >= 0")
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return MessagingPlan{}, err
	}
	provider := lc(spec.Provider)
	if provider == ProviderDigitalOcean {
		// B1 (pd-MIG-B1-QUEUE-STREAM-OPERATORS): SQS → RabbitMQ Cluster Operator on DOKS.
		// DO has no managed queue primitive; the operator pattern replaces the single-VM
		// mitigation. ClusterName is required — the operator must have somewhere to run.
		cluster := strings.TrimSpace(spec.ClusterName)
		if cluster == "" {
			return MessagingPlan{}, fmt.Errorf(
				"managed-queue: digitalocean replaces SQS with the RabbitMQ Cluster Operator on a " +
					"DOKS cluster (DO has no managed queue primitive) — cluster_name is required. " +
					"This is a hard plan-time error, never a silent fallback")
		}
		ns := strings.TrimSpace(spec.Namespace)
		if ns == "" {
			ns = defaultRabbitMQNS
		}
		replicas := spec.Replicas
		if replicas <= 0 {
			replicas = defaultRabbitMQReplicas
		}
		return MessagingPlan{
			Kind:                     KindQueue,
			Provider:                 provider,
			CSP:                      row.CSP,
			RegionName:               row.RegionName,
			CSPRegion:                row.CSPRegion,
			Name:                     canonicalName(spec.Name, "pyxcloud-queue"),
			FIFO:                     spec.FIFO,
			VisibilityTimeoutSeconds: spec.VisibilityTimeoutSeconds,
			MaxReceiveCount:          spec.MaxReceiveCount,
			ClusterName:              cluster,
			Namespace:                ns,
			Replicas:                 replicas,
			RendersHelm:              true,
			ResourceType:             "kubernetes_manifest",
		}, nil
	}
	if provider == ProviderLinode {
		return MessagingPlan{}, ErrComponentUnsupported{
			Component: TypeManagedQueue, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "Linode has no managed message-queue/broker primitive; use a queue on " +
				"AWS (SQS) or GCP (Pub/Sub topic+subscription), or run a self-managed broker on a " +
				"virtual-machine",
		}
	}
	if provider == ProviderIBM {
		// IBM Cloud has no managed point-to-point work-queue primitive (MQ on Cloud
		// is an exotic, dedicated-queue-manager product, not the cross-provider
		// queue this component models). Event Streams (Kafka) is a STREAM, not a
		// work queue, so it maps to event-streaming, not managed-queue.
		return MessagingPlan{}, ErrComponentUnsupported{
			Component: TypeManagedQueue, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "IBM Cloud has no managed work-queue primitive comparable to SQS/Pub-Sub " +
				"(MQ on Cloud is a dedicated queue-manager product, not a simple managed queue); use a " +
				"queue on AWS (SQS) or GCP (Pub/Sub), use an `event-streaming` component for IBM Event " +
				"Streams (Kafka), or run a self-managed broker on a virtual-machine",
		}
	}
	if provider == ProviderStackIt {
		// StackIt's broker (stackit_rabbitmq_instance) is provisioned by a service
		// plan_id (a project/region-specific UUID) that cannot be authored in the
		// catalog; surface a clean error rather than an unresolvable required field.
		return MessagingPlan{}, ErrComponentUnsupported{
			Component: TypeManagedQueue, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "StackIt RabbitMQ (stackit_rabbitmq_instance) requires a service plan_id (a " +
				"project/region-specific UUID) PyxCloud cannot resolve from the catalog; provision " +
				"it directly, or use AWS SQS / GCP Pub/Sub for a fully managed canonical queue",
		}
	}
	plan := MessagingPlan{
		Kind:                     KindQueue,
		Provider:                 provider,
		CSP:                      row.CSP,
		RegionName:               row.RegionName,
		CSPRegion:                row.CSPRegion,
		Name:                     canonicalName(spec.Name, "pyxcloud-queue"),
		FIFO:                     spec.FIFO,
		VisibilityTimeoutSeconds: spec.VisibilityTimeoutSeconds,
		MaxReceiveCount:          spec.MaxReceiveCount,
	}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_sqs_queue"
	case ProviderGCP:
		plan.ResourceType = "google_pubsub_subscription"
	case ProviderAzure:
		plan.ResourceType = "azurerm_servicebus_queue"
	case ProviderOracle:
		plan.ResourceType = "oci_queue_queue"
	case ProviderAlibaba:
		plan.ResourceType = "alicloud_message_service_queue"
	}
	return plan, nil
}

// TranslateStream resolves a StreamSpec into a concrete MessagingPlan. DO is a
// clean unsupported error (no managed streaming primitive).
func TranslateStream(ctx context.Context, cat RegionCatalog, spec StreamSpec) (MessagingPlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return MessagingPlan{}, fmt.Errorf("event-streaming: region (abstract pyx region_name) is required")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return MessagingPlan{}, fmt.Errorf("event-streaming: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if spec.Shards < 0 {
		return MessagingPlan{}, fmt.Errorf("event-streaming: shards must be >= 0 (0 = on-demand)")
	}
	if spec.RetentionHours < 0 {
		return MessagingPlan{}, fmt.Errorf("event-streaming: retention_hours must be >= 0")
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return MessagingPlan{}, err
	}
	provider := lc(spec.Provider)
	if provider == ProviderDigitalOcean {
		// B1 (pd-MIG-B1-QUEUE-STREAM-OPERATORS): Kinesis → Strimzi Kafka Operator on DOKS.
		// DO has no managed streaming primitive; the operator pattern replaces the single-VM
		// mitigation. ClusterName is required — the operator must have somewhere to run.
		cluster := strings.TrimSpace(spec.ClusterName)
		if cluster == "" {
			return MessagingPlan{}, fmt.Errorf(
				"event-streaming: digitalocean replaces Kinesis with the Strimzi Kafka operator on a " +
					"DOKS cluster (DO has no managed streaming primitive) — cluster_name is required. " +
					"This is a hard plan-time error, never a silent fallback")
		}
		ns := strings.TrimSpace(spec.Namespace)
		if ns == "" {
			ns = defaultKafkaNS
		}
		replicas := spec.Replicas
		if replicas <= 0 {
			replicas = defaultKafkaReplicas
		}
		retention := spec.RetentionHours
		if retention <= 0 {
			retention = defaultKafkaRetentionHours
		}
		return MessagingPlan{
			Kind:           KindStream,
			Provider:       provider,
			CSP:            row.CSP,
			RegionName:     row.RegionName,
			CSPRegion:      row.CSPRegion,
			Name:           canonicalName(spec.Name, "pyxcloud-stream"),
			Shards:         spec.Shards,
			RetentionHours: retention,
			ClusterName:    cluster,
			Namespace:      ns,
			Replicas:       replicas,
			RendersHelm:    true,
			ResourceType:   "kubernetes_manifest",
		}, nil
	}
	if provider == ProviderLinode {
		return MessagingPlan{}, ErrComponentUnsupported{
			Component: TypeEventStreaming, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "Linode has no managed event-streaming primitive; use AWS Kinesis or " +
				"GCP Pub/Sub, or run a self-managed broker (Kafka/Redpanda) on a virtual-machine",
		}
	}
	// IBM Event Streams (managed Kafka) IS a clean event-streaming primitive,
	// provisioned as an ibm_resource_instance (service=messagehub). Supported.
	if provider == ProviderStackIt {
		return MessagingPlan{}, ErrComponentUnsupported{
			Component: TypeEventStreaming, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "StackIt has no managed event-streaming primitive; use AWS Kinesis or GCP " +
				"Pub/Sub, or run a self-managed broker (Kafka/Redpanda) on a stackit_server",
		}
	}
	plan := MessagingPlan{
		Kind:           KindStream,
		Provider:       provider,
		CSP:            row.CSP,
		RegionName:     row.RegionName,
		CSPRegion:      row.CSPRegion,
		Name:           canonicalName(spec.Name, "pyxcloud-stream"),
		Shards:         spec.Shards,
		RetentionHours: spec.RetentionHours,
	}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_kinesis_stream"
	case ProviderGCP:
		plan.ResourceType = "google_pubsub_topic"
	case ProviderAzure:
		plan.ResourceType = "azurerm_eventhub"
	case ProviderOracle:
		plan.ResourceType = "oci_streaming_stream"
	case ProviderIBM:
		plan.ResourceType = "ibm_resource_instance"
	case ProviderAlibaba:
		plan.ResourceType = "alicloud_alikafka_instance"
	}
	return plan, nil
}

// CanonicalQueueType reports whether t names the managed-queue component.
func CanonicalQueueType(t string) (string, bool) {
	switch lc(t) {
	case TypeManagedQueue, TypeMessageQueue:
		return TypeManagedQueue, true
	}
	return "", false
}

// CanonicalStreamType reports whether t names the event-streaming component.
func CanonicalStreamType(t string) (string, bool) {
	switch lc(t) {
	case TypeEventStreaming, TypeEventBus:
		return TypeEventStreaming, true
	}
	return "", false
}
