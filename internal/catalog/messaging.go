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
//     - DigitalOcean: UNSUPPORTED. DO has no managed queue/broker primitive
//       (App Platform workers are not a queue). Clean plan-time error -> SQS/PubSub.
//
//   event-streaming / event-bus — an ordered, replayable, multi-consumer stream:
//     - AWS: aws_kinesis_stream (on-demand) — the streaming primitive.
//     - GCP: a google_pubsub_topic (Pub/Sub is GCP's stream + bus; consumers
//       attach their own subscriptions). One topic = the bus.
//     - DigitalOcean: UNSUPPORTED. No managed streaming primitive. Clean error.
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
		return MessagingPlan{}, ErrComponentUnsupported{
			Component: TypeManagedQueue, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "DigitalOcean has no managed message-queue/broker primitive; use a queue on " +
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
		return MessagingPlan{}, ErrComponentUnsupported{
			Component: TypeEventStreaming, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "DigitalOcean has no managed event-streaming primitive; use AWS Kinesis or " +
				"GCP Pub/Sub, or run a self-managed broker (Kafka/Redpanda) on a virtual-machine",
		}
	}
	// IBM Event Streams (managed Kafka) IS a clean event-streaming primitive,
	// provisioned as an ibm_resource_instance (service=messagehub). Supported.
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
	case ProviderIBM:
		plan.ResourceType = "ibm_resource_instance"
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
