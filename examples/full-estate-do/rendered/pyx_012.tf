data "digitalocean_kubernetes_cluster" "app-traces_cluster" {
  name = "backend"
}

resource "helm_release" "app-traces_otel_operator" {
  name       = "app-traces-otel-operator"
  repository = "https://open-telemetry.github.io/opentelemetry-helm-charts"
  chart      = "opentelemetry-operator"
  version    = "0.62.0"
  namespace  = "observability"
  create_namespace = true
  depends_on = [data.digitalocean_kubernetes_cluster.app-traces_cluster]
}

resource "helm_release" "app-traces_tempo_operator" {
  name       = "app-traces-tempo-operator"
  repository = "https://grafana.github.io/helm-charts"
  chart      = "tempo-operator"
  version    = "0.10.0"
  namespace  = "observability"
  create_namespace = true
  depends_on = [data.digitalocean_kubernetes_cluster.app-traces_cluster]
}

resource "kubernetes_manifest" "app-traces_tempostack" {
  manifest = {
    apiVersion = "tempo.grafana.com/v1alpha1"
    kind       = "TempoStack"
    metadata = {
      name      = "app-traces-tempo"
      namespace = "observability"
      labels    = { app = "tempo", pyxcloud = "true" }
    }
    spec = {
      storage = {
        secret = {
          name = "app-traces-tempo-storage"
          type = "s3"
        }
      }
      storageSize = "10Gi"
      retention = {
        global = {
          traces = "168h"
        }
      }
      template = {
        distributor = {
          component = {
            replicas = 1
          }
        }
        queryFrontend = {
          component = {
            replicas = 1
          }
        }
      }
    }
  }
  depends_on = [helm_release.app-traces_tempo_operator]
}

resource "kubernetes_manifest" "app-traces_collector" {
  manifest = {
    apiVersion = "opentelemetry.io/v1beta1"
    kind       = "OpenTelemetryCollector"
    metadata = {
      name      = "app-traces-otel-collector"
      namespace = "observability"
      labels    = { app = "otel-collector", pyxcloud = "true" }
    }
    spec = {
      mode     = "deployment"
      image    = "otel/opentelemetry-collector-contrib:0.103.1"
      replicas = 1
      config = {
        receivers = {
          otlp = {
            protocols = {
              grpc = { endpoint = "0.0.0.0:4317" }
              http = { endpoint = "0.0.0.0:4318" }
            }
          }
        }
        processors = {
          probabilistic_sampler = { sampling_percentage = 20 }
          batch                 = {}
        }
        exporters = {
          "otlp/tempo" = {
            endpoint = "tempo-app-traces-tempo-distributor.observability.svc.cluster.local:4317"
            tls      = { insecure = true }
          }
        }
        service = {
          pipelines = {
            traces = {
              receivers  = ["otlp"]
              processors = ["probabilistic_sampler", "batch"]
              exporters  = ["otlp/tempo"]
            }
          }
        }
      }
    }
  }
  depends_on = [helm_release.app-traces_otel_operator, kubernetes_manifest.app-traces_tempostack]
}
