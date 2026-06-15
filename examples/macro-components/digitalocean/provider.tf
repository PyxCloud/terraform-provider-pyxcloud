# DigitalOcean plan/validate harness for pd-TF-REST-LAMBDA. The concrete
# resources are GENERATED from ../macro.json by pyxnet-render into generated.tf.
# Several components (managed-queue, event-streaming, waf-service, secrets-manager)
# are genuinely UNSUPPORTED on DO and pyxnet-render emits a clean plan-time error
# for them (never an invented resource); only the supported ones (cache, dns-zone,
# serverless-function via App Platform) render here. Real apply requires
# DIGITALOCEAN_TOKEN; otherwise this is plan/validate only.
terraform {
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

provider "digitalocean" {}
