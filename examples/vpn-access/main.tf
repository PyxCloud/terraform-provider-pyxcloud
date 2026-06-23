# vpn-access signal example — the JIT VPN door as a declarative topology signal.
#
# Declaring `pyx_vpn_access` auto-wires the Just-In-Time corporate-VPN door instead
# of the manual PyxCloud/internal-vpn `add-peer.sh` + hand-written
# `jit-backing/terraform.tf`. The provider descends the signal into three coupled
# AWS pieces (catalog-driven, AWS-only):
#
#   1. aws_security_group "<name>-jit" — WireGuard UDP 51820, ingress EMPTY at rest
#      and OWNED BY THE KEYCLOAK SPI at runtime (lifecycle.ignore_changes = [ingress],
#      so the SPI and terraform never fight). Optional break-glass static allow.
#   2. aws_dynamodb_table "<name>-jit-allowlist" — the SPI's session->ingress-rule
#      store (hash key sessionId, TTL on ttlEpoch, PAY_PER_REQUEST, PITR).
#   3. aws_iam_policy + attachment — grants the Keycloak instance role exactly the
#      SG-ingress + DynamoDB actions the SPI needs.
#
# The output `<name>_jit_sg_id` is what the Keycloak JIT SPI's JIT_VPN_SG_ID env
# points at. A non-AWS `cloud` is a clean plan-time error (never an invented
# resource) — the JIT door is an AWS-native pattern.

terraform {
  required_providers {
    pyxcloud = { source = "registry.terraform.io/PyxCloud/pyxcloud" }
  }
}

provider "pyxcloud" {}

resource "pyxcloud_environment" "corp_vpn" {
  name   = "corp-vpn"
  cloud  = "aws"
  region = "Dublin" # abstract pyx region_name -> eu-west-1

  pyx_vpn_access = [{
    name          = "vpn"
    keycloak_role = "beta-keycloak-ec2-role" # IAM role NAME of the Keycloak instance running the JIT SPI

    # Optional: a break-glass CIDR allowed regardless of JIT (admin lockout safety).
    # Omit for a pure-JIT door that is dark at rest.
    break_glass_cidrs = ["203.0.113.7/32"]

    # Optional overrides (defaults shown):
    # wireguard_port         = 51820
    # allowlist_table        = "jit-allowlist"
    # point_in_time_recovery = true
    # vpc                    = "<sibling pyx_vpc name>"  # defaults to the account default VPC
  }]
}

output "jit_sg_id" {
  # Point the Keycloak JIT SPI env JIT_VPN_SG_ID at this.
  value = pyxcloud_environment.corp_vpn.outputs
}
