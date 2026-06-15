# Out-of-band inputs the GENERATED OCI macro resources reference by variable.
# PyxCloud's macro components do NOT own KMS keys, function images, or rotation
# functions (sibling components / CI wire those); these are plan-only placeholders.

variable "kms_key_id" {
  type        = string
  description = "OCID of the KMS key backing the vault secret (plan-only)."
  default     = "ocid1.key.oc1..exampleplaceholder"
}

variable "function_subnet_ids" {
  type        = list(string)
  description = "Private subnet OCIDs the Functions application binds to (plan-only)."
  default     = ["ocid1.subnet.oc1..exampleplaceholder"]
}

variable "function_image" {
  type        = string
  description = "OCIR image reference for the serverless-function (plan-only)."
  default     = "iad.ocir.io/namespace/repo/fn:latest"
}

variable "rotation_function_id" {
  type        = string
  description = "OCID of the rotation Function for secrets-manager rotation (plan-only)."
  default     = "ocid1.fnfunc.oc1..exampleplaceholder"
}

variable "node_image_id" {
  type        = string
  description = "OKE worker node image OCID (plan-only)."
  default     = "ocid1.image.oc1..exampleplaceholder"
}

variable "dns_view_id" {
  type        = string
  description = "Private DNS view OCID for a private dns-zone (plan-only)."
  default     = "ocid1.dnsview.oc1..exampleplaceholder"
}

variable "vcn_id" {
  type        = string
  description = "VCN OCID for components that attach to an existing VCN (plan-only)."
  default     = "ocid1.vcn.oc1..exampleplaceholder"
}

variable "subnet_id" {
  type        = string
  description = "Subnet OCID for components that attach to an existing subnet (plan-only)."
  default     = "ocid1.subnet.oc1..exampleplaceholder"
}

variable "load_balancer_id" {
  type        = string
  description = "Load balancer OCID a WAF attaches to when no LB is rendered (plan-only)."
  default     = "ocid1.loadbalancer.oc1..exampleplaceholder"
}
