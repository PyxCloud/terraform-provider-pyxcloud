package catalog

import (
	"fmt"
	"strings"
)

// render_messaging_operators.go — DigitalOcean operator-pattern rendering for
// queue and stream components (B1: pd-MIG-B1-QUEUE-STREAM-OPERATORS).
//
// SQS → RabbitMQ Cluster Operator on DOKS:
//   CORE: bitnami/rabbitmq-cluster-operator helm_release (upstream operator +
//         CRDs); the chart installs the controller, RBAC, and CRDs.
//   EXTRA: RabbitmqCluster CR (kubernetes_manifest) — the cluster the operator
//          reconciles; replicas=3 for HA (quorum queues need an odd quorum).
//
// Kinesis → Strimzi Kafka Operator on DOKS:
//   CORE: strimzi/strimzi-kafka-operator helm_release (CNCF, upstream operator).
//   EXTRA: Kafka CR (kubernetes_manifest) — KRaft mode, replicas=3, TLS listeners.
//
// All operator-pattern resources land on the existing DOKS cluster via the
// shared `data "digitalocean_kubernetes_cluster"` reference (renderOperatorComponent
// helper in operator.go).

// renderQueueDO renders the DigitalOcean managed-queue operator-pattern: the
// RabbitMQ Cluster Operator (CORE helm_release) + a RabbitmqCluster CR (EXTRA).
func renderQueueDO(p MessagingPlan) string {
	name := tfName(p.Name)
	clusterData := name + "_cluster"
	operatorRelease := name + "_rmq_operator"
	clusterCR := name + "_rmq_cluster"

	// ── CORE: RabbitMQ Cluster Operator (upstream Helm chart) ──
	core := []HelmReleaseSpec{
		{
			TFName:          operatorRelease,
			ReleaseName:     p.Name + "-rmq-operator",
			Repository:      rabbitmqOperatorRepo,
			Chart:           rabbitmqOperatorChart,
			Version:         rabbitmqOperatorVersion,
			Namespace:       p.Namespace,
			CreateNamespace: true,
			ClusterDataRef:  clusterData,
		},
	}

	// ── EXTRA: RabbitmqCluster CR the operator reconciles ──
	extra := []ManifestCR{
		{
			TFName:    clusterCR,
			Manifest:  renderRabbitmqClusterManifest(p),
			DependsOn: []string{"helm_release." + operatorRelease},
		},
	}

	return renderOperatorComponent(clusterData, p.ClusterName, core, extra)
}

// renderRabbitmqClusterManifest renders the EXTRA RabbitmqCluster CR body.
// The RabbitMQ Cluster Operator reconciles this into a StatefulSet + Service.
// replicas=3 gives a quorum-queue-capable cluster (HA). FIFO semantics are
// preserved via a quorum queue policy (classic queues are async by default).
func renderRabbitmqClusterManifest(p MessagingPlan) string {
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"rabbitmq.com/v1beta1\"\n")
	b.WriteString("    kind       = \"RabbitmqCluster\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name      = %q\n", p.Name+"-rmq")
	fmt.Fprintf(&b, "      namespace = %q\n", p.Namespace)
	b.WriteString("      labels    = { app = \"rabbitmq\", pyxcloud = \"true\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	fmt.Fprintf(&b, "      replicas = %d\n", p.Replicas)
	// TLS is enabled via the operator's TLS spec; secret created out-of-band
	// (e.g. via the cert-manager TLS certificate component).
	b.WriteString("      tls = {\n")
	fmt.Fprintf(&b, "        secretName           = %q\n", p.Name+"-rmq-tls")
	b.WriteString("        disableNonTLSListeners = true\n")
	b.WriteString("      }\n")
	// FIFO / ordering: enable quorum queues by default (replicated, ordered,
	// exactly-once delivery on publisher confirm). Classic queues remain available
	// for non-FIFO use cases via the management UI / AMQP connection args.
	if p.FIFO {
		b.WriteString("      rabbitmq = {\n")
		b.WriteString("        additionalConfig = \"default_queue_type = quorum\\n\"\n")
		b.WriteString("      }\n")
	}
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// renderStreamDO renders the DigitalOcean event-streaming operator-pattern: the
// Strimzi Kafka Operator (CORE helm_release) + a Kafka CR (EXTRA). Strimzi is
// the CNCF-graduated Kafka operator and the recommended replacement for Kinesis.
func renderStreamDO(p MessagingPlan) string {
	name := tfName(p.Name)
	clusterData := name + "_cluster"
	operatorRelease := name + "_strimzi_operator"
	kafkaCR := name + "_kafka"

	// ── CORE: Strimzi Kafka Operator (upstream Helm chart, CNCF) ──
	core := []HelmReleaseSpec{
		{
			TFName:          operatorRelease,
			ReleaseName:     p.Name + "-strimzi-operator",
			Repository:      strimziOperatorRepo,
			Chart:           strimziOperatorChart,
			Version:         strimziOperatorVersion,
			Namespace:       p.Namespace,
			CreateNamespace: true,
			Set: []HelmSet{
				// Watch all namespaces for Kafka CRs (simplest config for
				// single-cluster use; restrict to Namespace for multi-tenant).
				{Name: "watchAnyNamespace", Value: "true"},
			},
			ClusterDataRef: clusterData,
		},
	}

	// ── EXTRA: Kafka CR (KRaft mode, replicated brokers + TLS) ──
	extra := []ManifestCR{
		{
			TFName:    kafkaCR,
			Manifest:  renderStrimziKafkaManifest(p),
			DependsOn: []string{"helm_release." + operatorRelease},
		},
	}

	return renderOperatorComponent(clusterData, p.ClusterName, core, extra)
}

// renderStrimziKafkaManifest renders the EXTRA Kafka CR body (Strimzi API).
// KRaft mode (no ZooKeeper) with 3 combined broker+controller replicas for HA.
// TLS listeners on port 9093; plaintext listener on 9092 is disabled.
func renderStrimziKafkaManifest(p MessagingPlan) string {
	var b strings.Builder
	b.WriteString("  manifest = {\n")
	b.WriteString("    apiVersion = \"kafka.strimzi.io/v1beta2\"\n")
	b.WriteString("    kind       = \"Kafka\"\n")
	b.WriteString("    metadata = {\n")
	fmt.Fprintf(&b, "      name        = %q\n", p.Name+"-kafka")
	fmt.Fprintf(&b, "      namespace   = %q\n", p.Namespace)
	b.WriteString("      labels      = { app = \"kafka\", pyxcloud = \"true\" }\n")
	// Strimzi KRaft annotation: moves Kafka into ZooKeeper-free KRaft mode.
	b.WriteString("      annotations = { \"strimzi.io/kraft\" = \"enabled\", \"strimzi.io/node-pools\" = \"enabled\" }\n")
	b.WriteString("    }\n")
	b.WriteString("    spec = {\n")
	b.WriteString("      kafka = {\n")
	fmt.Fprintf(&b, "        version  = \"3.7.0\"\n")
	fmt.Fprintf(&b, "        replicas = %d\n", p.Replicas)
	// TLS listener only — no plaintext (SECURE BY DEFAULT).
	b.WriteString("        listeners = [{\n")
	b.WriteString("          name = \"tls\"\n")
	b.WriteString("          port = 9093\n")
	b.WriteString("          type = \"internal\"\n")
	b.WriteString("          tls  = true\n")
	b.WriteString("        }]\n")
	b.WriteString("        config = {\n")
	if p.RetentionHours > 0 {
		fmt.Fprintf(&b, "          \"log.retention.hours\"   = %q\n", fmt.Sprintf("%d", p.RetentionHours))
	}
	// Replication factor matches replica count for full durability.
	fmt.Fprintf(&b, "          \"default.replication.factor\" = %q\n", fmt.Sprintf("%d", p.Replicas))
	fmt.Fprintf(&b, "          \"min.insync.replicas\"        = %q\n", fmt.Sprintf("%d", max(p.Replicas-1, 1)))
	b.WriteString("          \"offsets.topic.replication.factor\" = \"3\"\n")
	b.WriteString("        }\n")
	b.WriteString("        storage = {\n")
	b.WriteString("          type = \"ephemeral\"\n")
	b.WriteString("        }\n")
	b.WriteString("      }\n")
	// entityOperator manages topics + users via CRs (KafkaTopic, KafkaUser).
	b.WriteString("      entityOperator = {\n")
	b.WriteString("        topicOperator = {}\n")
	b.WriteString("        userOperator  = {}\n")
	b.WriteString("      }\n")
	b.WriteString("    }\n")
	b.WriteString("  }\n")
	return b.String()
}

// max returns the larger of two ints (Go 1.21+ has built-in max; keep a local
// copy for broad compatibility with older toolchains in this repo).
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
