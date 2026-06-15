# Out-of-band inputs the GENERATED GCP macro resources reference by variable
# (the serverless-function source bucket/object and the secrets-manager rotation
# wiring). The caller supplies these; plan-only here.

variable "source_bucket" {
  type        = string
  description = "GCS bucket holding the Cloud Functions source archive."
  default     = "pyxcloud-fn-source"
}

variable "source_object" {
  type        = string
  description = "GCS object key for the Cloud Functions source archive."
  default     = "fn.zip"
}

variable "next_rotation_time" {
  type        = string
  description = "RFC3339 first rotation time for a rotating Secret Manager secret."
  default     = "2030-01-01T00:00:00Z"
}

variable "rotation_topic" {
  type        = string
  description = "Pub/Sub topic for Secret Manager rotation notifications."
  default     = ""
}
