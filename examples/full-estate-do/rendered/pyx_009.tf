resource "digitalocean_database_cluster" "jit-allowlist" {
  name       = "jit-allowlist"
  engine     = "redis"
  version    = "7"
  size       = "db-s-1vcpu-1gb"
  region     = "fra1"
  node_count = 2
  private_network_uuid = digitalocean_vpc.passo-estate-net.id
  tags = ["pyxcloud"]
}
