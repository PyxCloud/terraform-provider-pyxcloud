# Out-of-band inputs the generated alicloud macro resources reference by variable.
# These are NOT rendered by pyxcloud (they are account/region-scoped or
# secret-bearing): the alikafka placement vswitch + data-encryption KMS key, the
# WAF instance/domain/origin, the KMS secret value, and the Function Compute
# execution role + OSS code object. They carry throwaway defaults so `terraform
# validate` (and `plan`, with creds) runs without prompting; a real apply would
# supply real values out-of-band.

variable "alikafka_vswitch_id" {
  description = "VSwitch the alikafka (event-streaming) instance attaches to."
  type        = string
  default     = "vsw-placeholder"
}

variable "alikafka_kms_key_id" {
  description = "KMS key id used to encrypt alikafka broker data at rest."
  type        = string
  default     = "key-placeholder"
}

variable "waf_instance_id" {
  description = "WAF instance the protected domain is attached to (account-scoped)."
  type        = string
  default     = "waf-placeholder"
}

variable "waf_domain_name" {
  description = "The domain protected by WAF."
  type        = string
  default     = "waf.pyxcloud-plan-only.example.com"
}

variable "waf_origin_ip" {
  description = "Origin server IP the WAF forwards cleaned traffic to."
  type        = string
  default     = "10.0.1.10"
}

variable "secret_data" {
  description = "The KMS secret value (supplied out-of-band; never committed)."
  type        = string
  sensitive   = true
  default     = "plan-only-placeholder"
}

variable "fc_role_arn" {
  description = "RAM role the Function Compute function assumes."
  type        = string
  default     = "acs:ram::000000000000:role/pyxcloud-fc-exec"
}

variable "fc_code_bucket" {
  description = "OSS bucket holding the function code package."
  type        = string
  default     = "pyxcloud-fc-code"
}

variable "fc_code_object" {
  description = "OSS object key of the function code package."
  type        = string
  default     = "function.zip"
}
