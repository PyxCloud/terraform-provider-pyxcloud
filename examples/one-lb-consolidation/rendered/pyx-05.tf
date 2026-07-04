resource "digitalocean_loadbalancer" "pyx-edge-lb" {
  name   = "pyx-edge-lb"
  region = "fra1"
  vpc_uuid = digitalocean_vpc.pyx-edge-net.id

  forwarding_rule {
    entry_protocol  = "tcp"
    entry_port      = 443
    target_protocol = "tcp"
    target_port     = 443
  }

  healthcheck {
    protocol                 = "tcp"
    port                     = 443
    check_interval_seconds   = 30
    healthy_threshold        = 3
    unhealthy_threshold      = 3
  }

  droplet_tag = "pyx-edge"
}
