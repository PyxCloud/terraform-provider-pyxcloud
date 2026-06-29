# Deploy Gates

This document describes the deploy gates enforced by the Passo delivery pipeline. Each gate must pass before a deployment proceeds to the next stage.

## IaC Security Scan Gate

**Purpose:** Prevent deployment of infrastructure-as-code (IaC) templates that contain known security misconfigurations, exposed secrets, or policy violations.

**Implementation:** The gate is implemented in `internal/iacsecscan/scanner.go` and invoked by `internal/driftdeploygate/gate.go`. It runs a static analysis scan against the rendered Terraform plan (JSON) produced by the `pyxenv-render` or `pyxnet-render` commands.

**Scan Rules:** The scanner applies a set of built-in rules defined in `internal/iacsecscan/scanner.go` and extensible via `internal/driftsignalrule/rule.go`. Rules cover:
- Hardcoded secrets (AWS access keys, database passwords, API tokens)
- Publicly exposed S3 buckets / storage containers
- Overly permissive security group rules (0.0.0.0/0 on sensitive ports)
- Use of default KMS keys
- Missing encryption at rest / in transit
- IAM roles with wildcard actions
- Use of deprecated or insecure resource types

**Integration:** The gate is triggered automatically during the CI pipeline (`ci.pipeline.json`) after the plan is generated and before the apply step. It can also be run manually via:
# PyxCloud Provider — Topology Deployment Specification

This document describes how a `pyxcloud_topology` resource is provisioned into a real cloud environment during `terraform apply`.

## 1. Deployment Modes

The provider supports two primary deployment modes:

### Mode A: Provider-Native (Default)
In this mode, the deployment is executed where the Terraform CLI is run (e.g., local workstation, runner, or CI pipeline).
1. The provider sends the topology configuration to the PyxCloud API to translate it into concrete, provider-specific Terraform configuration files (`.tf`).
2. The provider executes the translated configuration locally using an embedded Terraform runner.
3. Credentials are resolved natively from the standard environment chain (e.g., `AWS_ACCESS_KEY_ID`, `GOOGLE_APPLICATION_CREDENTIALS`, etc.).
4. The deployment state is written directly back to the Terraform state.

### Mode B: Backend-Driven Deployment (Optional)
For environments where local execution is restricted, the deployment can be delegated to the PyxCloud backend.
1. The provider requests the backend to perform the deployment.
2. The backend uses the credentials stored in the corresponding `account_binding` to provision the resources.
3. The deployment is executed via secure server-side pipeline workers.

## 2. Security and Gateways

To protect infrastructure, deployments via Mode B require explicit authorization:

- **Service Authorization**: Non-interactive deployments must use a service account with the appropriate deployment roles.
- **Payload Integrity**: Every deployment request is cryptographically bound to the exact payload configuration. The backend verifies the signature and parameters before execution to prevent configuration tampering.
- **Policy Enforcement**: Target providers and regions are checked against organizational policies and allowed configurations prior to provisioning.
