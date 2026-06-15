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

# A canonical topology: provider-independent components + sizing, pinned to a
# deployment provider and abstract macro-region.
resource "pyxcloud_topology" "web" {
  name     = "web-stack"
  provider = "aws"
  region   = "EU West"

  components {
    name  = "app"
    type  = "virtual-machine-scale-group"
    count = 3
    vm {
      architecture = "x86_64"
      cpu          = "2"
      ram          = "4"
      os_name      = "ubuntu"
    }
  }

  components {
    name = "edge"
    type = "load-balancer"
  }

  components {
    name = "db"
    type = "managed-database"
  }
}

# Compare the equivalent topology priced across providers and regions — the
# Terraform analogue of the console "Compare" page.
data "pyxcloud_compare" "options" {
  name = "web-stack"

  components {
    name  = "app"
    type  = "virtual-machine-scale-group"
    count = 3
    vm {
      architecture = "x86_64"
      cpu          = "2"
      ram          = "4"
      os_name      = "ubuntu"
    }
  }

  components {
    name = "edge"
    type = "load-balancer"
  }

  components {
    name = "db"
    type = "managed-database"
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
