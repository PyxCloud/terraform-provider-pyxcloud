data "digitalocean_kubernetes_cluster" "app-traces_cluster" {
  name = "backend"
}

resource "kubernetes_manifest" "app-traces_tempo_deployment" {
  manifest = {
    apiVersion = "apps/v1"
    kind       = "Deployment"
    metadata = {
      name      = "app-traces-tempo"
      namespace = "observability"
      labels    = { app = "tempo", pyxcloud = "true" }
    }
    spec = {
      replicas = 1
      selector = { matchLabels = { app = "tempo" } }
      template = {
        metadata = { labels = { app = "tempo" } }
        spec = {
          containers = [{
            name  = "tempo"
            image = "grafana/tempo:2.4.1"
            args  = ["-config.file=/etc/tempo/tempo.yaml", "-storage.trace.local.path=/var/tempo", "-config.expand-env=true"]
            ports = [
              { containerPort = 4317, name = "otlp-grpc" },
              { containerPort = 3200, name = "http-query" },
            ]
            env = [{ name = "TEMPO_RETENTION", value = "168h" }]
          }]
        }
      }
    }
  }
}

resource "kubernetes_manifest" "app-traces_tempo_service" {
  manifest = {
    apiVersion = "v1"
    kind       = "Service"
    metadata = {
      name      = "app-traces-tempo"
      namespace = "observability"
    }
    spec = {
      selector = { app = "tempo" }
      ports = [
        { name = "otlp-grpc", port = 4317, targetPort = 4317 },
        { name = "http-query", port = 3200, targetPort = 3200 },
      ]
    }
  }
  depends_on = [kubernetes_manifest.app-traces_tempo_deployment]
}

resource "kubernetes_manifest" "app-traces_collector_config" {
  manifest = {
    apiVersion = "v1"
    kind       = "ConfigMap"
    metadata = {
      name      = "app-traces-otel-collector"
      namespace = "observability"
    }
    data = {
      "collector.yaml" = <<-OTELCONFIG
receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
      http:
        endpoint: 0.0.0.0:4318
processors:
  probabilistic_sampler:
    sampling_percentage: 20
  batch: {}
exporters:
  otlp/tempo:
    endpoint: app-traces-tempo.observability.svc.cluster.local:4317
    tls:
      insecure: true
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [probabilistic_sampler, batch]
      exporters: [otlp/tempo]
OTELCONFIG
    }
  }
}

resource "kubernetes_manifest" "app-traces_collector_deployment" {
  manifest = {
    apiVersion = "apps/v1"
    kind       = "Deployment"
    metadata = {
      name      = "app-traces-otel-collector"
      namespace = "observability"
      labels    = { app = "otel-collector", pyxcloud = "true" }
    }
    spec = {
      replicas = 1
      selector = { matchLabels = { app = "otel-collector" } }
      template = {
        metadata = { labels = { app = "otel-collector" } }
        spec = {
          containers = [{
            name  = "otel-collector"
            image = "otel/opentelemetry-collector-contrib:0.103.1"
            args  = ["--config=/conf/collector.yaml"]
            ports = [
              { containerPort = 4317, name = "otlp-grpc" },
              { containerPort = 4318, name = "otlp-http" },
            ]
            volumeMounts = [{ name = "config", mountPath = "/conf" }]
          }]
          volumes = [{ name = "config", configMap = { name = "app-traces-otel-collector" } }]
        }
      }
    }
  }
  depends_on = [kubernetes_manifest.app-traces_collector_config, kubernetes_manifest.app-traces_tempo_service]
}

resource "kubernetes_manifest" "app-traces_collector_service" {
  manifest = {
    apiVersion = "v1"
    kind       = "Service"
    metadata = {
      name      = "app-traces-otel-collector"
      namespace = "observability"
    }
    spec = {
      selector = { app = "otel-collector" }
      ports = [
        { name = "otlp-grpc", port = 4317, targetPort = 4317 },
        { name = "otlp-http", port = 4318, targetPort = 4318 },
      ]
    }
  }
  depends_on = [kubernetes_manifest.app-traces_collector_deployment]
}
