resource "digitalocean_reserved_ip" "sso-lb" {
  region     = "fra1"
  droplet_id = digitalocean_droplet.sso-1.id
}
