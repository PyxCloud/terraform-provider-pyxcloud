# Out-of-band inputs the GENERATED AWS macro resources reference by variable.
# PyxCloud's macro components do NOT own IAM or rotation Lambdas (sibling
# access-policy / serverless components do); these are wired by the caller. The
# round-trip harness supplies real values for the cheap types it actually applies
# (the Lambda execution role) and leaves the rest as plan-only placeholders.

variable "lambda_role_arn" {
  type        = string
  description = "Execution role ARN for the serverless-function (Lambda)."
  default     = ""
}

variable "eks_cluster_role_arn" {
  type        = string
  description = "IAM role ARN for the EKS control plane (plan-only)."
  default     = ""
}

variable "eks_node_role_arn" {
  type        = string
  description = "IAM role ARN for the EKS managed node group (plan-only)."
  default     = ""
}

variable "rotation_lambda_arn" {
  type        = string
  description = "ARN of the rotation Lambda for secrets-manager rotation (plan-only)."
  default     = ""
}
