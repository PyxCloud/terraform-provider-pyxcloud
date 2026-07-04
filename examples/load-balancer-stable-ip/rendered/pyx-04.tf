resource "digitalocean_droplet" "sso-1" {
  name   = "sso-1"
  image  = "ubuntu-24-04-x64"
  region = "fra1"
  size   = "s-2vcpu-4gb"
  vpc_uuid = digitalocean_vpc.edge-net.id
  tags = ["pyxcloud"]
}
