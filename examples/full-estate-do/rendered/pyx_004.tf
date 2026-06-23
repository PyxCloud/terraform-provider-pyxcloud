resource "digitalocean_kubernetes_cluster" "vpn" {
  name    = "vpn"
  region  = "fra1"
  version = "1.30"
  vpc_uuid = digitalocean_vpc.passo-estate-net.id
  node_pool {
    name       = "vpn-pool"
    size       = "s-2vcpu-2gb"
    auto_scale = true
    min_nodes  = 1
    max_nodes  = 1
    node_count = 1
    tags = ["pyxcloud"]
  }
  tags = ["pyxcloud"]
}
