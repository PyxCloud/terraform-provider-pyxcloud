# DigitalOcean round-trip harness for pd-TF-MDB (managed-database cluster).
#
# Concrete resources (digitalocean_vpc + digitalocean_firewall +
# digitalocean_database_cluster) are GENERATED from ../db.json by `pyxnet-render`
# into generated.tf. DO managed clusters are encrypted at rest by default (no
# toggle); HA is a 2-node cluster.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../db.json -provider digitalocean -component network          >  generated.tf
#   pyxnet-render -fixture ../db.json -provider digitalocean -component security-group   >> generated.tf
#   pyxnet-render -fixture ../db.json -provider digitalocean -component managed-database >> generated.tf
#   terraform init && terraform plan
#   terraform apply -auto-approve     # gated, real creds (DIGITALOCEAN_TOKEN)
#   terraform destroy -auto-approve
#
# NOTE: the production fixture emits a lifecycle { prevent_destroy = true } guard
# on the cluster (DO has no in-place deletion-protection flag), so a disposable
# round-trip needs a test fixture with deletion_protection=false to tear down.

terraform {
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

provider "digitalocean" {}
