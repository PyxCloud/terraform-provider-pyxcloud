# PyxCloud Provider-to-Provider Migration Engine — Overview

This document outlines the workflow and user-facing interface for migrating PyxCloud environments between different cloud providers (e.g., AWS, GCP, Azure, DigitalOcean).

## 1. High-Level Concept

The migration engine allows users to switch the underlying cloud provider of a deployment topology. The migration is planned, coordinated, and executed with strong consistency, data safety, and verification guarantees.

The migration covers:
- Compute resources and process state.
- Filesystems and persistent volumes.
- Managed database schemas and data.
- Object and blob storage.
- Secret management mappings.
- Event queues and stream offsets.

## 2. User-Facing Workflow

The migration process follows a safe three-phase workflow:

### Phase 1: Plan
When a provider switch is requested in the topology, the PyxCloud control plane prepares a migration plan. This plan details the required steps, resource creation on the target provider, and data transfer orchestration.

### Phase 2: Execute
The migration is executed step-by-step. The system performs initial data synchronization while the source environment is active to minimize downtime, followed by a coordinated cutover.
- **Data Consistency**: Data is verified using cryptographic checksums during transfer.
- **Idempotent & Resumable**: If a network interruption occurs, the execution resumes from the last successful checkpoint without corrupting or duplicating resources.

### Phase 3: Verify and Cutover
Once the target environment is fully synchronized, the system verifies its health and consistency before updating the DNS or load balancer records.
- **Rollback Guarantee**: If target verification fails or the target environment is unhealthy, the migration rolls back safely, and traffic remains routed to the active source environment.

## 3. Configuration

The migration behavior is configured in the Terraform provider via the `migration` block:

```hcl
resource "pyxcloud_environment" "prod" {
  name     = "production"
  provider = "aws" # change to "gcp" to trigger migration

  migration {
    enabled      = true
    max_duration = "2h"
    dry_run      = false
  }
}
```

- `enabled`: Activates migration support when switching providers.
- `max_duration`: Maximum allowable time for the migration to complete before timing out and rolling back.
- `dry_run`: Simulates the migration planning phase and verifies credentials without executing the actual cutover.
