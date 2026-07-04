resource "digitalocean_vpc" "edge-net" {
  name     = "edge-net"
  region   = "fra1"
  ip_range = "10.0.1.0/24"
}
