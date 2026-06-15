# DigitalOcean round-trip harness for pd-TF-EC2-VM.
#
# Concrete resources (digitalocean_vpc + digitalocean_droplet) are GENERATED
# from ../vm.json by `pyxnet-render` into generated.tf. DO firewalls attach to
# droplets (not the VPC); the VM fixture keeps the droplet in the VPC.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../vm.json -provider digitalocean -component network         >  generated.tf
#   pyxnet-render -fixture ../vm.json -provider digitalocean -component virtual-machine >> generated.tf
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

# Token comes from DIGITALOCEAN_TOKEN. Region (Frankfurt -> fra1), droplet size
# (s-2vcpu-4gb) and image (ubuntu-24-04-x64) are baked into the generated droplet.
provider "digitalocean" {}
