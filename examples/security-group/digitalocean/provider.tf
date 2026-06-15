# DigitalOcean round-trip harness for pd-TF-SG.
#
# Concrete resource (digitalocean_firewall) is GENERATED from ../sg-do.json by
# `pyxnet-render -component security-group`. DO firewalls attach to droplets/tags
# (not to a VPC), so the firewall stands alone here; droplets join it later.
#
# DigitalOcean firewalls support only tcp/udp/icmp (no "all" protocol), so the
# DO fixture (sg-do.json) declares an explicit tcp egress rule rather than the
# `all` egress used in the AWS/GCP fixture — the translator rejects `all` for DO
# at plan time (a hard error, never a silent fallback).
#
# Test flow (SPEC §6):
#   pyxnet-render -fixture ../sg-do.json -provider digitalocean -component security-group > generated.tf
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

provider "digitalocean" {}
