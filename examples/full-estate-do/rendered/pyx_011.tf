resource "digitalocean_loadbalancer" "edge-lb" {
  name   = "edge-lb"
  region = "fra1"
  vpc_uuid = digitalocean_vpc.passo-estate-net.id

  forwarding_rule {
    entry_protocol  = "https"
    entry_port      = 443
    target_protocol = "https"
    target_port     = 443
  }

  healthcheck {
    protocol                 = "http"
    port                     = 8080
    path                     = "/q/health"
    check_interval_seconds   = 30
    healthy_threshold        = 3
    unhealthy_threshold      = 3
  }

  droplet_tag = "pyxcloud"
}

resource "kubernetes_manifest" "edge-lb_ingress" {
  manifest = {
    apiVersion = "networking.k8s.io/v1"
    kind       = "Ingress"
    metadata = {
      name      = "edge-lb"
      namespace = "default"
      annotations = {
        "kubernetes.io/ingress.class" = "nginx"
        "nginx.ingress.kubernetes.io/whitelist-source-range" = "10.8.0.0/24"
      }
    }
    spec = {
      rules = [
        {
          host = "admin.passo.build"
          http = {
            paths = [
              {
                path     = "/"
                pathType = "Prefix"
                backend = {
                  service = {
                    name = "sso-svc"
                    port = { number = 443 }
                  }
                }
              },
            ]
          }
        },
        {
          host = "app.passo.build"
          http = {
            paths = [
              {
                path     = "/"
                pathType = "Prefix"
                backend = {
                  service = {
                    name = "backend-svc"
                    port = { number = 443 }
                  }
                }
              },
            ]
          }
        },
      ]
    }
  }
}
