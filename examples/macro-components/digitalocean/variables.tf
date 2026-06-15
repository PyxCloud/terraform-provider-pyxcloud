# Out-of-band inputs the GENERATED DO macro resources reference by variable. The
# serverless-function (App Platform function component) is deployed from a source
# repo the caller supplies; plan-only here.

variable "function_repo_url" {
  type        = string
  description = "Git clone URL for the App Platform function source."
  default     = "https://github.com/example/fn.git"
}

variable "function_branch" {
  type        = string
  description = "Git branch for the App Platform function source."
  default     = "main"
}
