# Environment pipeline example — the terraform-native replacement for a hand-written
# per-provider stack. `terraform apply` of pyxcloud_environment translates the canonical
# topology to concrete provider terraform (backend /api/translate) and applies it with the
# AMBIENT provider env credentials (AWS_*), exactly how our existing per-provider scripts
# authenticate. Mode A — no account_binding, no backend-held creds.
#
# Run by .github/workflows/env-pipeline.yml (build provider -> dev_override -> plan/apply).

terraform {
  required_providers {
    pyxcloud = {
      source = "registry.terraform.io/PyxCloud/pyxcloud"
    }
  }
}

provider "pyxcloud" {
  # endpoint + machine auth come from env in CI:
  #   PYXCLOUD_ENDPOINT, PYXCLOUD_CLIENT_ID, PYXCLOUD_CLIENT_SECRET, PYXCLOUD_TOKEN_URL
  # (the provider authenticates as its own OAuth2.1 client — no human login).
}

# A minimal real environment: one VM in AWS / Dublin (abstract pyx region_name ->
# eu-west-1). `cloud` (not `provider` — that's a reserved terraform meta-argument).
resource "pyxcloud_environment" "demo" {
  name   = "pyx-pipeline-demo"
  cloud  = "aws"
  region = "Dublin"

  components = [
    {
      name = "app"
      type = "virtual-machine"
      vm = {
        architecture = "x86_64"
        cpu          = "2"
        ram          = "4"
        os_name      = "ubuntu"
      }
    },
    {
      name = "app-role"
      type = "iam"
      iam = {
        assume_service      = "ec2.amazonaws.com"
        instance_profile    = true
        managed_policy_arns = ["arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"]
      }
    },
  ]
}

output "environment_outputs" {
  value = pyxcloud_environment.demo.outputs
}
