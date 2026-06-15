# Azure (wave-2) round-trip harness for pd-TF-REGION-VPC.
#
# The concrete resources (azurerm_resource_group + azurerm_virtual_network +
# azurerm_subnet) are GENERATED from the canonical fixture (../place.json) by
# `pyxnet-render -provider azure`, written next to this file as generated.tf.
# This file only pins the cloud provider. Azure derives its region per-resource
# (location) from the catalog-resolved csp_region (Frankfurt -> germanywestcentral),
# so no provider-level region is pinned here.
#
# Test flow (SPEC §6):
#   pyxnet-render -fixture ../place.json -provider azure > generated.tf
#   terraform init
#   terraform validate                      # no creds needed
#   ARM_SUBSCRIPTION_ID=... terraform plan   # gated, real creds
#   ARM_SUBSCRIPTION_ID=... terraform apply -auto-approve
#   ARM_SUBSCRIPTION_ID=... terraform destroy -auto-approve

terraform {
  required_providers {
    azurerm = {
      source  = "hashicorp/azurerm"
      version = "~> 4.0"
    }
  }
}

provider "azurerm" {
  features {}
  # subscription_id is supplied out-of-band (ARM_SUBSCRIPTION_ID) for a real
  # plan/apply; validate needs no credentials.
}
