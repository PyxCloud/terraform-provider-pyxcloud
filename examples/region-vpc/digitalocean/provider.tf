# DigitalOcean round-trip harness for pd-TF-REGION-VPC.
#
# Concrete resource (digitalocean_vpc) is GENERATED from ../place.json by
# `pyxnet-render -provider digitalocean` into generated.tf.
#
# Test flow (SPEC §6):
#   pyxnet-render -fixture ../place.json -provider digitalocean > generated.tf
#   export DIGITALOCEAN_TOKEN=...
#   terraform init && terraform plan
#   terraform apply -auto-approve     # gated, real creds
#   terraform destroy -auto-approve

terraform {
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

# Token comes from DIGITALOCEAN_TOKEN. The region is baked into the generated
# digitalocean_vpc (Frankfurt -> fra1).
provider "digitalocean" {}
