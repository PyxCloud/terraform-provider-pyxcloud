data "digitalocean_kubernetes_cluster" "nightly_cluster" {
  name = "backend"
}

resource "kubernetes_cron_job_v1" "nightly" {
  metadata {
    name      = "nightly"
    namespace = "default"
    labels    = { pyxcloud = "true" }
  }
  spec {
    schedule                      = "0 3 * * *"
    concurrency_policy            = "Forbid"
    successful_jobs_history_limit = 3
    failed_jobs_history_limit     = 1
    job_template {
      metadata {}
      spec {
        template {
          metadata {}
          spec {
            container {
              name  = "nightly"
              image = "registry.passo.build/maint:latest"
            }
            restart_policy = "OnFailure"
          }
        }
      }
    }
  }
}
