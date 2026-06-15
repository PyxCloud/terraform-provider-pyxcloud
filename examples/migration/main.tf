# Provider→provider migration of a macro place (pd-TF-PROVIDER-MIGRATION).
#
# The pyxcloud_migration resource is a THIN, OPAQUE client: it ferries a sealed
# (encrypted) execution bundle from the PyxCloud backend to a confidential runtime
# and reports coarse status. The migration know-how (CRIU / rsync / DB / secret /
# queue / DNS sequencing) is a backend industrial secret sealed inside the bundle
# and NEVER present in the provider — Terraform only ever sees ciphertext + a
# coarse phase / percent / verdict.
#
# Typical trigger: a `pyxcloud_compare` run shows a cheaper provider for a place,
# and you cut over to it. The migration runs inside a confidential runtime that
# `auto` selects (strongest available: confidential-container -> hardware-TEE ->
# sealed-WASM floor); decryption happens only inside that attested, sealed
# boundary.

terraform {
  required_providers {
    pyxcloud = {
      source = "PyxCloud/pyxcloud"
    }
  }
}

provider "pyxcloud" {
  # endpoint defaults to https://passo.build
  # token falls back to the PYXCLOUD_TOKEN environment variable.
}

resource "pyxcloud_migration" "checkout_to_gcp" {
  place           = "checkout"
  source_provider = "aws"
  target_provider = "gcp"

  # Canonical topology JSON for the place — forwarded opaquely to the backend
  # planner; the provider does not interpret it for migration purposes.
  source_topology = jsonencode({
    name = "checkout"
  })

  migration {
    enabled = true

    # auto (default): pick the strongest confidential runtime the host offers.
    # Override to "local-tee" or "confidential-container" to pin a substrate.
    confidential_runtime = "auto"

    # Attestation root the runtime/backend use (forwarded; the provider does not
    # itself verify attestation).
    attestation_endpoint = "https://attest.passo.build"

    max_duration = "2h"

    # Set true to plan/verify the migration without performing the cutover.
    dry_run = false
  }
}

# Coarse, opacity-safe outputs — never the migration method.
output "migration_verdict" {
  value = pyxcloud_migration.checkout_to_gcp.verdict # success | rolled-back | failed
}

output "migration_substrate" {
  value = pyxcloud_migration.checkout_to_gcp.substrate # e.g. sealed-wasm | hardware-tee | confidential-container
}

output "migration_phase" {
  value = pyxcloud_migration.checkout_to_gcp.phase
}
