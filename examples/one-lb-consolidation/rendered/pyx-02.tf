resource "digitalocean_vpc" "pyx-edge-net" {
  name     = "pyx-edge-net"
  region   = "fra1"
  ip_range = "10.0.1.0/24"
}
