resource "digitalocean_vpc" "passo-estate-net" {
  name     = "passo-estate-net"
  region   = "fra1"
  ip_range = "10.0.1.0/24"
}
