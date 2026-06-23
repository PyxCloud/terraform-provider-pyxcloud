data "digitalocean_kubernetes_cluster" "app-tls_cluster" {
  name = "backend"
}

resource "helm_release" "app-tls_certmanager_operator" {
  name       = "cert-manager"
  repository = "https://charts.jetstack.io"
  chart      = "cert-manager"
  version    = "v1.15.1"
  namespace  = "cert-manager"
  create_namespace = true
  set = [
    { name = "installCRDs", value = "true" },
  ]
  depends_on = [data.digitalocean_kubernetes_cluster.app-tls_cluster]
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
  depends_on = [helm_release.app-tls_certmanager_operator]
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
  depends_on = [helm_release.app-tls_certmanager_operator, kubernetes_manifest.app-tls_issuer]
}
