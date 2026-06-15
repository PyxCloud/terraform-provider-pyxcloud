# DigitalOcean round-trip harness for pd-TF-ASG (virtual-machine-scale-group).
#
# DigitalOcean has NO native VM autoscaling primitive. PyxCloud does not invent a
# non-existent resource, so `pyxnet-render ... -component scale-group` for
# digitalocean is a HARD plan-time error directing the user to a
# `managed-kubernetes` component (DOKS node-pool autoscaling) instead. This
# directory exists to document that decision and to let the round-trip harness
# assert the clean error (it never produces a generated.tf for DO).
#
# Decision (see PR body / SPEC 5.4): the catalog marks DigitalOcean
# `virtual_machine` rows with supports_autoscale=false; there is no clean
# 1:1 mapping to an autoscaling group, so we surface ErrAutoscaleUnsupported
# rather than mapping to a fixed-size droplet pool that would silently NOT
# autoscale.
#
# Test flow (SPEC 6):
#   pyxnet-render -fixture ../asg.json -provider digitalocean -component scale-group
#     -> exits non-zero with: "virtual-machine-scale-group is not supported on
#        provider \"digitalocean\" ... use a managed-kubernetes component"

terraform {
  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.0"
    }
  }
}

provider "digitalocean" {}
