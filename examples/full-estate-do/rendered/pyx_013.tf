data "digitalocean_kubernetes_cluster" "app-tls_cluster" {
  name = "backend"
}

resource "kubernetes_manifest" "app-tls_issuer" {
  manifest = {
    apiVersion = "cert-manager.io/v1"
    kind       = "ClusterIssuer"
    metadata = {
      name = "letsencrypt-prod"
    }
    spec = {
      acme = {
        server = "https://acme-v02.api.letsencrypt.org/directory"
        email  = "ops@passo.build"
        privateKeySecretRef = {
          name = "letsencrypt-prod-account-key"
        }
        solvers = [{
          http01 = {
            ingress = {
              class = "nginx"
            }
          }
        }]
      }
    }
  }
}

resource "kubernetes_manifest" "app-tls_certificate" {
  manifest = {
    apiVersion = "cert-manager.io/v1"
    kind       = "Certificate"
    metadata = {
      name      = "app-tls"
      namespace = "default"
    }
    spec = {
      secretName = "app-tls-tls"
      issuerRef = {
        name = "letsencrypt-prod"
        kind = "ClusterIssuer"
      }
      dnsNames = ["app.passo.build", "admin.passo.build"]
    }
  }
  depends_on = [kubernetes_manifest.app-tls_issuer]
}
