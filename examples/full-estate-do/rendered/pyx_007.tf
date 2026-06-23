resource "digitalocean_kubernetes_cluster" "backend" {
  name    = "backend"
  region  = "fra1"
  version = "1.30"
  vpc_uuid = digitalocean_vpc.passo-estate-net.id
  node_pool {
    name       = "backend-pool"
    size       = "s-2vcpu-4gb"
    auto_scale = true
    min_nodes  = 1
    max_nodes  = 1
    node_count = 1
    tags = ["pyxcloud"]
  }
  tags = ["pyxcloud"]
}
