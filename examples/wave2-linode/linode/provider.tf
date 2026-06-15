# Linode (Akamai) round-trip harness for wave-2 (pd-TF-W2-LINODE).
#
# The concrete resources (linode_vpc / linode_instance / linode_firewall /
# linode_nodebalancer / linode_database_postgresql / linode_object_storage_bucket
# / linode_domain / linode_lke_cluster) are GENERATED from the canonical fixture
# (../place.json) by `pyxnet-render -provider linode`, written next to this file
# as generated.tf. This file only pins the cloud provider + the secrets that are
# managed out-of-band (never committed).
#
# Test flow (SPEC §6):
#   for c in network security-group virtual-machine load-balancer \
#            managed-database object-storage dns-zone managed-kubernetes; do
#     pyxnet-render -fixture ../place.json -provider linode -component $c
#   done > generated.tf
#   terraform init
#   terraform validate           # offline: schema + reference validation
#   LINODE_TOKEN=... terraform plan   # GATED: real creds; SKIPPED in CI (no creds)
#
# NO real apply/destroy is performed here: Linode credentials are not available in
# this environment, so only `terraform validate` (and `plan` where a token exists)
# is exercised — stated explicitly per the task.

terraform {
  required_providers {
    linode = {
      source  = "linode/linode"
      version = "~> 2.0"
    }
  }
}

# The Linode provider reads LINODE_TOKEN from the environment; no token is set in
# CI, so plan/apply are skipped and only `terraform validate` runs offline.
provider "linode" {}

# Out-of-band secrets / inputs (never committed): the instance root password, the
# managed-database access list, and the DNS SOA email. Supplied via TF_VAR_* or a
# tfvars file at apply time.
variable "linode_root_pass" {
  type        = string
  sensitive   = true
  description = "Root password for linode_instance, injected at apply (Vault / throwaway)."
  default     = "ReplaceMeAtApplyTime!42"
}

variable "db_allow_list" {
  type        = list(string)
  description = "CIDR allow-list for the managed PostgreSQL cluster (private by default)."
  default     = ["10.0.0.0/16"]
}

variable "dns_soa_email" {
  type        = string
  description = "SOA email required for a Linode master DNS zone."
  default     = "hostmaster@example.com"
}
