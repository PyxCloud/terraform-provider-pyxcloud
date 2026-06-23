resource "digitalocean_spaces_bucket" "assets" {
  name          = "assets-58339b7745"
  region        = "fra1"
  acl           = "private"
  force_destroy = false
  versioning {
    enabled = true
  }
  # server-side encryption (AES256) is enabled at rest by default on DO Spaces
  lifecycle_rule {
    id      = "abort-mpu"
    enabled = true
    abort_incomplete_multipart_upload_days = 7
  }
  lifecycle_rule {
    id      = "expire-tmp"
    enabled = true
    prefix  = "tmp/"
    expiration {
      days = 30
    }
  }
}

resource "digitalocean_spaces_bucket_policy" "assets" {
  region = "fra1"
  bucket = digitalocean_spaces_bucket.assets.name
  policy = <<-PYXIAMPOLICY
{"Version":"2012-10-17","Statement":[]}
PYXIAMPOLICY
  
}

# NOTE: server access logging (target "audit-logs") has no DO Spaces equivalent; front the bucket with a CDN/edge log pipeline if access logs are required.
