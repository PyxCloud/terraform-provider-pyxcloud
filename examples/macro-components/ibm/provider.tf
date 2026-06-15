# IBM Cloud round-trip harness for pd-TF-REST-LAMBDA (the remaining macro
# components), wave-2. Concrete resources are GENERATED from ../macro.json by
# pyxnet-render. This file pins the provider + region and declares the
# out-of-band, account-specific inputs the IBM macro renderers reference by
# variable (resource group, CIS / DNS Services / Secrets Manager instance ids).
#
# Supported on IBM (rendered): cache (ibm_database redis), event-streaming
# (ibm_resource_instance messagehub), dns-zone (ibm_dns_zone private /
# ibm_cis_domain public), waf-service (ibm_cis_waf_group), managed-kubernetes
# (ibm_container_vpc_cluster), secrets-manager (ibm_sm_arbitrary_secret),
# serverless-function (ibm_code_engine_project + _app), object-storage
# (ibm_resource_instance COS + ibm_cos_bucket).
# UNSUPPORTED on IBM (clean plan-time error, never invented): managed-queue (no
# managed work-queue primitive), cdn-service (no origin-fronting CDN; CIS caching
# is domain-scoped).
#
# Region MUST equal the catalog-resolved csp_region (Frankfurt -> eu-de).

terraform {
  required_providers {
    ibm = {
      source  = "IBM-Cloud/ibm"
      version = "~> 1.70"
    }
  }
}

provider "ibm" {
  region = "eu-de"
}

variable "ibm_resource_group_id" {
  type    = string
  default = "00000000000000000000000000000000"
}

variable "ibm_ssh_key_id" {
  type    = string
  default = "r010-00000000-0000-0000-0000-000000000000"
}

variable "ibm_dns_instance_id" {
  type        = string
  description = "IBM Cloud DNS Services instance GUID (private dns-zone)."
  default     = "00000000-0000-0000-0000-000000000000"
}

variable "ibm_cis_id" {
  type        = string
  description = "IBM Cloud Internet Services instance CRN (public dns-zone / WAF)."
  default     = "crn:v1:bluemix:public:internet-svcs:global:a/x::"
}

variable "ibm_cis_domain_id" {
  type    = string
  default = "0000000000000000000000000000000000"
}

variable "ibm_cis_waf_package_id" {
  type    = string
  default = "package-id"
}

variable "ibm_cis_waf_group_id" {
  type    = string
  default = "group-id"
}

variable "ibm_sm_instance_id" {
  type        = string
  description = "IBM Cloud Secrets Manager instance GUID."
  default     = "00000000-0000-0000-0000-000000000000"
}

variable "secret_payload" {
  type      = string
  default   = "placeholder"
  sensitive = true
}
