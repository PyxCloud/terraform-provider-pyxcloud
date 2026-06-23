# garbage_collection = true (DO runs GC server-side via the registry API;
# there is no Terraform sub-resource — trigger it out-of-band post-apply).
resource "digitalocean_container_registry" "app-images" {
  name                   = "app-images-fe5f00037e"
  subscription_tier_slug = "professional"
  region                 = "fra1"
}
