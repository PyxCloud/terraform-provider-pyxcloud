terraform {
  required_providers {
    pyxcloud = {
      source = "PyxCloud/pyxcloud"
    }
  }
}

provider "pyxcloud" {
  endpoint = "https://passo.build"
  # token = "..."  # or export PYXCLOUD_TOKEN
}

# A canonical topology: provider-independent pyx_* components + sizing, pinned to a
# deployment provider and abstract macro-region.
resource "pyxcloud_topology" "web" {
  name     = "web-stack"
  cloud    = "aws"
  region   = "Frankfurt" # abstract pyx region_name; resolved to a csp_region via the catalog

  # Abstract network for the place (pd-TF-REGION-VPC): provider-neutral VPC CIDR
  # + subnet CIDRs. The provider resolves region -> csp_region from the catalog
  # and derives multi-AZ subnets, exposed back as `network_plan`.
  network = {
    cidr    = "10.0.0.0/16"
    subnets = ["10.0.1.0/24", "10.0.2.0/24", "10.0.3.0/24"]
  }

  # Abstract security-group for the place (pd-TF-SG): canonical `expose` ports +
  # explicit ingress/egress rules, attached to the network above. Resolved to
  # aws_security_group(_rule) / google_compute_firewall / digitalocean_firewall.
  # The description is ASCII-sanitised at plan time (AWS rejects non-ASCII).
  security_group = {
    description = "web tier - public HTTP/HTTPS, SSH from VPC"
    expose      = [80, 443]
    rules = [
      { direction = "ingress", protocol = "tcp", from_port = 22, to_port = 22, cidrs = ["10.0.0.0/16"] },
      { direction = "egress", protocol = "all", cidrs = ["0.0.0.0/0"] },
    ]
  }

  pyx_autoscale_virtual_machine_group {
    name  = "app"
    count = 3
    architecture = "x86_64"
    cpu          = "2"
    ram          = "4"
    os_name      = "ubuntu"
  }

  pyx_load_balancer {
    name = "edge"
  }

  pyx_database {
    name = "db"
  }
}

# Compare the equivalent topology priced across providers and regions — the
# Terraform analogue of the console "Compare" page.
data "pyxcloud_compare" "options" {
  name = "web-stack"

  pyx_autoscale_virtual_machine_group {
    name  = "app"
    count = 3
    architecture = "x86_64"
    cpu          = "2"
    ram          = "4"
    os_name      = "ubuntu"
  }

  pyx_load_balancer {
    name = "edge"
  }

  pyx_database {
    name = "db"
  }

  candidates {
    provider = "aws"
    region   = "EU West"
  }
  candidates {
    provider = "gcp"
    region   = "EU West"
  }
  candidates {
    provider = "digitalocean"
    region   = "EU West"
  }
}

output "all_options" {
  value = data.pyxcloud_compare.options.results
}

output "cheapest" {
  value = data.pyxcloud_compare.options.cheapest
}

# The catalog-resolved concrete network plan (csp_region + multi-AZ subnets).
output "network_plan" {
  value = pyxcloud_topology.web.network_plan
}

# The catalog-resolved concrete security-group plan (ASCII description + rules).
output "security_group_plan" {
  value = pyxcloud_topology.web.security_group_plan
}
