resource "digitalocean_firewall" "pyx-edge-sg" {
  name = "pyx-edge-sg"

  inbound_rule {
    protocol   = "tcp"
    port_range = "443"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol   = "tcp"
    port_range = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol   = "udp"
    port_range = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol   = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}
