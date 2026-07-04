resource "digitalocean_droplet_autoscale" "pyx-edge" {
  name = "pyx-edge"

  config {
    min_instances          = 2
    max_instances          = 2
    target_cpu_utilization = 0.6
  }

  droplet_template {
    size               = "s-1vcpu-2gb"
    region             = "fra1"
    image              = "ubuntu-24-04-x64"
    vpc_uuid           = digitalocean_vpc.pyx-edge-net.id
    tags               = ["pyxcloud", "pyx-edge"]
    ssh_keys           = ["57496891"]
    with_droplet_agent = true
    user_data          = <<-PYXUSERDATA
#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y nginx-full
# One-LB SNI host-router: L4 passthrough (no TLS termination), routes :443 by SNI.
# Preserves end-to-end TLS to each origin; matches the old per-service LB behaviour
# while consolidating them into a single ingress. pd-ONE-LB-CONSOLIDATION.
cat >/etc/nginx/nginx.conf <<'NGINX'
include /etc/nginx/modules-enabled/*.conf;
user www-data;
worker_processes auto;
events { worker_connections 2048; }
stream {
  map $ssl_preread_server_name $pyx_upstream {
    auth.pyxcloud.io         sso_prod;
    vpn-auth.pyxcloud.io     sso_staging;
    staging-api.pyxcloud.io  backend_staging;
    mcp.passo.build          mcp_staging;
    staging-mcp.passo.build  mcp_staging;
    default                  sink;
  }
  upstream sso_prod        { server 164.92.248.51:443; server 207.154.231.103:443; }
  upstream sso_staging     { server 206.189.53.146:443; }
  upstream backend_staging { server 104.248.245.240:443; }
  upstream mcp_staging     { server 178.128.201.212:443; }
  upstream sink            { server 127.0.0.1:9; }
  server {
    listen 443;
    ssl_preread on;
    proxy_pass $pyx_upstream;
    proxy_connect_timeout 5s;
  }
}
NGINX
nginx -t && systemctl enable nginx && systemctl restart nginx
PYXUSERDATA
  
  }
}
