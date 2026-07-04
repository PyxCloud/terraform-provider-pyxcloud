package catalog

import (
	"fmt"
	"strings"
)

// platform_bootstrap_sast_do.go — pd-MIG-CUTOVER-F2-02 (sast; EPIC-AWS-TO-DO-MIGRATION).
//
// platform_asgs.go expresses the SAST scanner as a canonical
// `virtual-machine-scale-group` of 1. But the SAST runner is NOT a user_data
// port of the SSO/backend kind: on AWS (infrastructure/sast-asg.tf) it is a
// job-queue worker — the backend enqueues a repo.zip to S3 under
// `scan-jobs/<job>/input/repo.zip`, sets the ASG desired-capacity to 1 to wake a
// runner, the runner polls S3, pulls the scanner super-image from ECR, runs
// Semgrep + OSV, writes results back to S3 under `scan-jobs/<job>/output/…`, and
// finally calls `autoscaling:SetDesiredCapacity 0` on itself to scale the ASG
// back to zero. It is dispatch-driven, self-terminating batch compute.
//
// This file ports that runner to DigitalOcean, keeping the SAME contract (the
// `scan-jobs/<job>/input/repo.zip` layout, the Semgrep+OSV super-image, the
// self-scale-down at the end) but swapping the three AWS primitives for their DO
// equivalents:
//
//   - ECR image            -> DO Container Registry image
//                             registry.digitalocean.com/pyx-registry/pyx-sast:latest
//                             (docker login registry.digitalocean.com with a DO
//                             registry read token; the DO API token works as the
//                             password, username = the token too).
//   - S3 job queue         -> DigitalOcean Spaces (S3-compatible). The runner
//                             polls Spaces with the aws CLI pointed at the Spaces
//                             endpoint (--endpoint-url https://<region>.digitaloceanspaces.com)
//                             using Spaces access/secret keys. Same key layout.
//   - ASG SetDesiredCapacity -> DO droplet_autoscale API. The runner scales its
//                             OWN pool back down via the DO API
//                             (PUT /v2/droplets/autoscale/<pool>) with a DO API
//                             token. See the LIMITATION note below.
//
// LIMITATION (documented in docs/cutover/SAST-REARCH.md): a DO
// digitalocean_droplet_autoscale pool cannot truly scale to ZERO the way an ASG
// can — min_instances must be >= 1 (TranslateScaleGroup already lifts a zero min
// to 1 for DO, and the DO API rejects min_instances=0). So "scale down" means
// scale back to the floor (1), not to 0. The self-scale target is therefore
// configurable (SastScaleDownTo, default 1). The cost implication is that the
// SAST pool is always-on at one small droplet rather than idling at zero; the
// canonical service sizing (2vCPU/4GiB) keeps that floor cheap.
//
// SECURITY: like platform_bootstrap_sso.go, NO secret VALUE is inlined. The
// Spaces keys, the DO registry token, and the DO API token are referenced by
// Terraform variable NAME (${var.<x>}); the operator wires those vars to Vault /
// the secret source. The script never embeds a literal credential.

// SastDOBootstrapSpec is the typed, provider-neutral input for the DigitalOcean
// SAST runner bootstrap. Every AWS interpolation from sast-asg.tf's
// sast_runner_user_data is lifted to an explicit field; the secret fields name
// the Terraform variable that holds the secret (NOT the value).
type SastDOBootstrapSpec struct {
	// Environment is the deploy environment (e.g. "beta"); drives the default
	// Spaces bucket + the droplet_autoscale pool name. Required.
	Environment string

	// RegistryImage is the fully-qualified DO Container Registry image the runner
	// pulls. Defaults to registry.digitalocean.com/pyx-registry/pyx-sast:latest.
	RegistryImage string

	// SpacesBucket is the Spaces bucket that holds the scan-jobs queue. Defaults
	// to "pyx-sast-jobs-fra1" (a dedicated jobs bucket; pyx-artifacts-fra1 is the
	// documented reuse alternative). The job key layout matches AWS:
	// scan-jobs/<job>/input/repo.zip and scan-jobs/<job>/output/{semgrep,osv}_output.json.
	SpacesBucket string
	// SpacesRegion is the Spaces region slug (e.g. "fra1"); drives the
	// S3-compatible endpoint https://<region>.digitaloceanspaces.com. Defaults to
	// "fra1".
	SpacesRegion string

	// SelfPoolName is the digitalocean_droplet_autoscale pool the runner scales
	// itself down on when the queue drains. Defaults to "<env>-sast" (matching the
	// canonical scale-group name the renderer emits). This is a NAME the runner
	// resolves to a pool id via the DO API at runtime.
	SelfPoolName string
	// ScaleDownTo is the min/max the runner sets the pool to when the queue is
	// empty. DO forbids 0, so this defaults to 1 (the floor). Set to a value > 1
	// only for a warm pool.
	ScaleDownTo int

	// --- secret VARIABLE NAMES (never values) ---

	// SpacesAccessKeyVar / SpacesSecretKeyVar name the Terraform variables holding
	// the Spaces S3-compatible access/secret keys. Default "do_spaces_access_key" /
	// "do_spaces_secret_key".
	SpacesAccessKeyVar string
	SpacesSecretKeyVar string
	// RegistryTokenVar names the variable holding the DO registry read token used
	// for `docker login registry.digitalocean.com` (the DO API token works).
	// Default "do_registry_token".
	RegistryTokenVar string
	// APITokenVar names the variable holding the DO API token the runner uses to
	// call the droplet_autoscale API for self-scale-down. Default "do_api_token".
	APITokenVar string
}

// withDefaults fills the production-faithful defaults for any unset field so
// callers can pass an almost-empty spec (just Environment) and still get the
// canonical wiring.
func (s SastDOBootstrapSpec) withDefaults() SastDOBootstrapSpec {
	def := func(v, d string) string {
		if strings.TrimSpace(v) == "" {
			return d
		}
		return v
	}
	s.RegistryImage = def(s.RegistryImage, "registry.digitalocean.com/pyx-registry/pyx-sast:latest")
	s.SpacesBucket = def(s.SpacesBucket, "pyx-sast-jobs-fra1")
	s.SpacesRegion = def(s.SpacesRegion, "fra1")
	if strings.TrimSpace(s.SelfPoolName) == "" && strings.TrimSpace(s.Environment) != "" {
		s.SelfPoolName = strings.TrimSpace(s.Environment) + "-sast"
	}
	if s.ScaleDownTo < 1 {
		// DO droplet_autoscale min_instances must be >= 1 (cannot scale to 0 like
		// an ASG). Clamp the self-scale-down target to the floor.
		s.ScaleDownTo = 1
	}
	s.SpacesAccessKeyVar = def(s.SpacesAccessKeyVar, "do_spaces_access_key")
	s.SpacesSecretKeyVar = def(s.SpacesSecretKeyVar, "do_spaces_secret_key")
	s.RegistryTokenVar = def(s.RegistryTokenVar, "do_registry_token")
	s.APITokenVar = def(s.APITokenVar, "do_api_token")
	return s
}

// SastDOBootstrapVariableNames returns, in deterministic order, the Terraform
// variable names this bootstrap references, partitioned plain vs sensitive so
// the assembler/CLI emits the matching `variable "<x>" {}` blocks (the
// credential-bearing ones marked sensitive) and the rendered .tf is
// self-contained and `terraform validate`s.
func (s SastDOBootstrapSpec) SastDOBootstrapVariableNames() (plain []string, sensitive []string) {
	s = s.withDefaults()
	// No plain vars today: the runner reads only secrets by variable; the bucket /
	// region / pool / image are literals baked at render time. Kept as a return
	// for symmetry with SSOBootstrapVariableNames and future plain inputs.
	plain = []string{}
	sensitive = []string{
		s.SpacesAccessKeyVar, s.SpacesSecretKeyVar, s.RegistryTokenVar, s.APITokenVar,
	}
	return plain, sensitive
}

// RenderSastDOBootstrapUserData renders the canonical DigitalOcean SAST runner
// cloud-init as a bash script with `${var.<x>}` placeholders for the secrets. It
// is a faithful port of sast-asg.tf's sast_runner_user_data with the three AWS
// primitives swapped for DO equivalents (ECR->DO registry, S3->Spaces,
// ASG-SetDesiredCapacity->DO droplet_autoscale API). The returned string is
// meant to be placed into AssembleScaleGroup.UserDataByProvider["digitalocean"]
// for the `sast` service — so ONLY a DigitalOcean placement gets this bootstrap;
// AWS keeps the ECR/S3/ASG runner.
func RenderSastDOBootstrapUserData(spec SastDOBootstrapSpec) (string, error) {
	s := spec.withDefaults()
	if strings.TrimSpace(s.Environment) == "" {
		return "", fmt.Errorf("sast-do-bootstrap: environment is required (drives the Spaces bucket and the droplet_autoscale pool name)")
	}
	v := func(name string) string { return "${var." + name + "}" }
	endpoint := fmt.Sprintf("https://%s.digitaloceanspaces.com", s.SpacesRegion)

	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	w("#!/bin/bash")
	w("# Canonical DigitalOcean SAST runner bootstrap — ported from")
	w("# infrastructure/sast-asg.tf sast_runner_user_data by the abstract provider")
	w("# (pd-MIG-CUTOVER-F2-02). Job-queue worker: poll Spaces, pull the DO registry")
	w("# scanner image, run Semgrep + OSV, write results to Spaces, self-scale-down.")
	w("# Secrets are Terraform variables, never inlined.")
	w("export DEBIAN_FRONTEND=noninteractive")
	w("")
	w("# Wait for apt/dpkg locks to release")
	w("while fuser /var/lib/dpkg/lock-frontend >/dev/null 2>&1; do")
	w("  echo \"Waiting for other apt-get instance to exit...\"")
	w("  sleep 3")
	w("done")
	w("")
	w("# Install Docker, the S3-compatible client (aws CLI) & base utilities")
	w("sudo apt-get update -y")
	w("sudo apt-get install -y unzip curl jq docker.io")
	w("sudo systemctl enable --now docker")
	w("")
	w("# aws CLI v2 (used against the Spaces S3-compatible endpoint)")
	w("curl \"https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip\" -o \"awscliv2.zip\"")
	w("unzip -q awscliv2.zip")
	w("sudo ./aws/install")
	w("")
	w("# --- DO-specific wiring (the three swaps from the AWS runner) ---")
	w("REGISTRY_IMAGE=%q", s.RegistryImage)
	w("BUCKET=%q", s.SpacesBucket)
	w("SPACES_ENDPOINT=%q", endpoint)
	w("SPACES_REGION=%q", s.SpacesRegion)
	w("POOL_NAME=%q", s.SelfPoolName)
	w("SCALE_DOWN_TO=%d", s.ScaleDownTo)
	w("")
	w("# Spaces S3-compatible credentials (aws CLI reads AWS_* env; Spaces keys go here).")
	w("export AWS_ACCESS_KEY_ID=\"%s\"", v(s.SpacesAccessKeyVar))
	w("export AWS_SECRET_ACCESS_KEY=\"%s\"", v(s.SpacesSecretKeyVar))
	w("export AWS_DEFAULT_REGION=\"$SPACES_REGION\"")
	w("# aws s3 against Spaces: always pass the endpoint.")
	w("s3s() { aws --endpoint-url \"$SPACES_ENDPOINT\" \"$@\"; }")
	w("")
	w("# Log in to the DO Container Registry and pull the scanner super-image.")
	w("# The DO API / registry read token works as BOTH username and password.")
	w("echo \"%s\" | docker login registry.digitalocean.com -u \"%s\" --password-stdin", v(s.RegistryTokenVar), v(s.RegistryTokenVar))
	w("docker pull \"$REGISTRY_IMAGE\"")
	w("")
	w("echo \"SAST Runner initialized. Polling Spaces bucket $BUCKET for jobs...\"")
	w("")
	w("# Loop until all pending jobs are finished (same contract as the AWS runner).")
	w("while true; do")
	w("  ALL_KEYS=$(s3s s3api list-objects-v2 --bucket \"$BUCKET\" --prefix \"scan-jobs/\" --query \"Contents[].Key\" --output text 2>/dev/null || true)")
	w("")
	w("  if [ -z \"$ALL_KEYS\" ] || [ \"$ALL_KEYS\" = \"None\" ]; then")
	w("    echo \"No objects found in Spaces.\"")
	w("    break")
	w("  fi")
	w("")
	w("  # Job IDs that have input/repo.zip but lack output/semgrep_output.json.")
	w("  PENDING_JOBS=\"\"")
	w("  for key in $ALL_KEYS; do")
	w("    if [[ \"$key\" == *\"input/repo.zip\" ]]; then")
	w("      JOB_ID=$(echo \"$key\" | cut -d'/' -f2)")
	w("      if ! echo \"$ALL_KEYS\" | grep -q \"scan-jobs/$JOB_ID/output/semgrep_output.json\"; then")
	w("        PENDING_JOBS=\"$PENDING_JOBS $JOB_ID\"")
	w("      fi")
	w("    fi")
	w("  done")
	w("")
	w("  if [ -z \"$PENDING_JOBS\" ]; then")
	w("    echo \"No pending jobs.\"")
	w("    break")
	w("  fi")
	w("")
	w("  for JOB_ID in $PENDING_JOBS; do")
	w("    echo \"Starting job $JOB_ID...\"")
	w("    mkdir -p \"/tmp/$JOB_ID\"")
	w("")
	w("    if s3s s3 cp \"s3://$BUCKET/scan-jobs/$JOB_ID/input/repo.zip\" \"/tmp/$JOB_ID/repo.zip\"; then")
	w("      unzip -q \"/tmp/$JOB_ID/repo.zip\" -d \"/tmp/$JOB_ID/src\"")
	w("")
	w("      # Semgrep")
	w("      docker run --rm -v \"/tmp/$JOB_ID/src:/src\" --entrypoint \"/usr/local/bin/semgrep\" \"$REGISTRY_IMAGE\" \\")
	w("        scan --json --quiet --disable-version-check --metrics off --config \"/opt/pyx-rules\" > \"/tmp/$JOB_ID/semgrep_output.json\" 2>/dev/null || echo '{\"results\":[]}' > \"/tmp/$JOB_ID/semgrep_output.json\"")
	w("")
	w("      # OSV Scanner")
	w("      docker run --rm -v \"/tmp/$JOB_ID/src:/src\" --entrypoint \"/usr/local/bin/osv-scanner\" \"$REGISTRY_IMAGE\" \\")
	w("        --json --experimental-offline \"/src\" > \"/tmp/$JOB_ID/osv_output.json\" 2>/dev/null || echo '{\"results\":[]}' > \"/tmp/$JOB_ID/osv_output.json\"")
	w("")
	w("      # Upload outputs back to Spaces (same key layout as AWS).")
	w("      s3s s3 cp \"/tmp/$JOB_ID/semgrep_output.json\" \"s3://$BUCKET/scan-jobs/$JOB_ID/output/semgrep_output.json\"")
	w("      s3s s3 cp \"/tmp/$JOB_ID/osv_output.json\" \"s3://$BUCKET/scan-jobs/$JOB_ID/output/osv_output.json\"")
	w("    fi")
	w("")
	w("    rm -rf \"/tmp/$JOB_ID\"")
	w("    break")
	w("  done")
	w("")
	w("  sleep 10")
	w("done")
	w("")
	w("# --- Self-scale-down via the DO droplet_autoscale API ---")
	w("# The AWS runner ran `aws autoscaling set-desired-capacity ... 0`. On DO the")
	w("# equivalent is a PUT to the droplet_autoscale pool. NB: DO forbids")
	w("# min_instances=0, so \"scale down\" means back to the floor ($SCALE_DOWN_TO,")
	w("# default 1), NOT zero. Resolve the pool id by name, then set min=max=floor.")
	w("echo \"All jobs processed. Scaling pool $POOL_NAME down to $SCALE_DOWN_TO...\"")
	w("DO_API=\"https://api.digitalocean.com/v2/droplets/autoscale\"")
	w("DO_TOKEN=\"%s\"", v(s.APITokenVar))
	w("POOL_ID=$(curl -sf -H \"Authorization: Bearer $DO_TOKEN\" \"$DO_API\" \\")
	w("  | jq -r --arg n \"$POOL_NAME\" '.autoscale_pools[]? | select(.name==$n) | .id' | head -n1)")
	w("if [ -n \"$POOL_ID\" ] && [ \"$POOL_ID\" != \"null\" ]; then")
	w("  curl -sf -X PUT -H \"Authorization: Bearer $DO_TOKEN\" -H \"Content-Type: application/json\" \\")
	w("    \"$DO_API/$POOL_ID\" \\")
	w("    -d \"{\\\"config\\\":{\\\"min_instances\\\":$SCALE_DOWN_TO,\\\"max_instances\\\":$SCALE_DOWN_TO,\\\"target_cpu_utilization\\\":0.6}}\" \\")
	w("    && echo \"Scaled $POOL_NAME to $SCALE_DOWN_TO.\" || echo \"WARN: scale-down PUT failed.\"")
	w("else")
	w("  echo \"WARN: could not resolve droplet_autoscale pool id for $POOL_NAME; leaving pool as-is.\"")
	w("fi")

	return b.String(), nil
}
