resource "digitalocean_kubernetes_cluster" "obs" {
  name    = "obs"
  region  = "fra1"
  version = "1.30"
  vpc_uuid = digitalocean_vpc.passo-estate-net.id
  node_pool {
    name       = "obs-pool"
    size       = "s-4vcpu-8gb"
    auto_scale = true
    min_nodes  = 1
    max_nodes  = 1
    node_count = 1
    tags = ["pyxcloud"]
  }
  tags = ["pyxcloud"]
}
