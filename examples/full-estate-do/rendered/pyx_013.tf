data "digitalocean_kubernetes_cluster" "app-monitoring_cluster" {
  name = "backend"
}

resource "helm_release" "app-monitoring_kube_prometheus_stack" {
  name       = "app-monitoring-kube-prometheus-stack"
  repository = "https://prometheus-community.github.io/helm-charts"
  chart      = "kube-prometheus-stack"
  version    = "61.3.2"
  namespace  = "observability"
  create_namespace = true
  set = [
    { name = "prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues", value = "false" },
    { name = "prometheus.prometheusSpec.podMonitorSelectorNilUsesHelmValues", value = "false" },
    { name = "prometheus.prometheusSpec.ruleSelectorNilUsesHelmValues", value = "false" },
  ]
  depends_on = [data.digitalocean_kubernetes_cluster.app-monitoring_cluster]
}

resource "helm_release" "app-monitoring_loki" {
  name       = "app-monitoring-loki"
  repository = "https://grafana.github.io/helm-charts"
  chart      = "loki"
  version    = "6.6.4"
  namespace  = "observability"
  create_namespace = true
  set = [
    { name = "deploymentMode", value = "SingleBinary" },
    { name = "loki.commonConfig.replication_factor", value = "1" },
    { name = "loki.storage.type", value = "filesystem" },
  ]
  depends_on = [data.digitalocean_kubernetes_cluster.app-monitoring_cluster]
}

resource "kubernetes_manifest" "app-monitoring_scrape_backend" {
  manifest = {
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "ServiceMonitor"
    metadata = {
      name      = "app-monitoring-backend"
      namespace = "observability"
      labels    = { app = "pyx-monitoring", pyxcloud = "true" }
    }
    spec = {
      selector = {
        matchLabels = {
          "app" = "backend"
        }
      }
      endpoints = [
        {
          port     = "metrics"
          path     = "/q/metrics"
          interval = "30s"
        }
      ]
    }
  }
  depends_on = [helm_release.app-monitoring_kube_prometheus_stack]
}

resource "kubernetes_manifest" "app-monitoring_alerts" {
  manifest = {
    apiVersion = "monitoring.coreos.com/v1"
    kind       = "PrometheusRule"
    metadata = {
      name      = "app-monitoring-alerts"
      namespace = "observability"
      labels    = { app = "pyx-monitoring", pyxcloud = "true" }
    }
    spec = {
      groups = [
        {
          name  = "app-monitoring.alarms"
          rules = [
            {
              alert = "backend-cpu-high"
              expr  = "node_cpu_high_ratio > 0.8"
              for   = "180s"
              labels = {
                severity = "warning"
              }
              annotations = {
                summary = "backend-cpu-high threshold breached"
                source  = "migrated from CloudWatch AWS/EC2/node_cpu_high_ratio"
              }
            },
            {
              alert = "backend-5xx"
              expr  = "http_server_requests_5xx_rate > 5"
              for   = "300s"
              labels = {
                severity = "critical"
              }
              annotations = {
                summary = "backend-5xx threshold breached"
                source  = "migrated from CloudWatch AWS/ApplicationELB/http_server_requests_5xx_rate"
              }
            },
          ]
        }
      ]
    }
  }
  depends_on = [helm_release.app-monitoring_kube_prometheus_stack]
}

resource "kubernetes_manifest" "app-monitoring_ds_loki" {
  manifest = {
    apiVersion = "grafana.integreatly.org/v1beta1"
    kind       = "GrafanaDatasource"
    metadata = {
      name      = "app-monitoring-loki"
      namespace = "observability"
    }
    spec = {
      instanceSelector = { matchLabels = { dashboards = "grafana" } }
      datasource = {
        name   = "Loki"
        type   = "loki"
        access = "proxy"
        url    = "http://app-monitoring-loki-gateway.observability.svc.cluster.local"
      }
    }
  }
  depends_on = [helm_release.app-monitoring_kube_prometheus_stack, helm_release.app-monitoring_loki]
}

resource "kubernetes_manifest" "app-monitoring_ds_tempo" {
  manifest = {
    apiVersion = "grafana.integreatly.org/v1beta1"
    kind       = "GrafanaDatasource"
    metadata = {
      name      = "app-monitoring-tempo"
      namespace = "observability"
    }
    spec = {
      instanceSelector = { matchLabels = { dashboards = "grafana" } }
      datasource = {
        name   = "Tempo"
        type   = "tempo"
        access = "proxy"
        url    = "http://tempo-app-traces-tempo-query-frontend.observability.svc.cluster.local:3200"
      }
    }
  }
  depends_on = [helm_release.app-monitoring_kube_prometheus_stack]
}
