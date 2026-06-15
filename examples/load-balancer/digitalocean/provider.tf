# DigitalOcean round-trip harness for pd-TF-LB (load-balancer).
#
# A digitalocean_loadbalancer fronts droplets selected by tag. DigitalOcean has
# NO native VM autoscaling primitive (see pd-TF-ASG), so a DO load-balancer is
# generated WITHOUT a scale-group: the network (digitalocean_vpc), firewall
# (digitalocean_firewall) and digitalocean_loadbalancer are rendered, and the LB
# targets droplets carrying the "pyxcloud" tag (droplet_tag). The actual droplets
# would come from a fixed `virtual-machine` set or a managed-kubernetes node pool.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../lb.json -provider digitalocean -component network        >  generated.tf
#   pyxnet-render -fixture ../lb.json -provider digitalocean -component security-group >> generated.tf
#   pyxnet-render -fixture ../lb.json -provider digitalocean -component load-balancer  >> generated.tf
#   terraform init && terraform plan
#   terraform apply -auto-approve     # gated, real creds (DIGITALOCEAN_TOKEN)
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
