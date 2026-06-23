# pyxcloud mitigation: digitalocean has no managed "secrets-manager" — self-hosting Vault (secrets-manager) on a VM using container image hashicorp/vault:latest
resource "digitalocean_droplet" "app-secrets-1" {
  name   = "app-secrets-1"
  image  = "ubuntu-24-04-x64"
  region = "fra1"
  size   = "s-2vcpu-4gb"
  vpc_uuid = digitalocean_vpc.passo-estate-net.id
  user_data = <<-PYXUSERDATA
#!/bin/bash
set -euo pipefail
# pyxcloud self-host: Vault (secrets-manager)
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y docker.io
systemctl enable --now docker
docker run -d --restart=always -p 8200:8200 --name pyx-service hashicorp/vault:latest
PYXUSERDATA
  
  tags = ["pyxcloud"]
}
