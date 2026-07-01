package catalog

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"
)

// do_baseline_test.go — pd-MIG-CUTOVER-F0-01 (EPIC-AWS-TO-DO-MIGRATION).
//
// Proves the DigitalOcean ACCOUNT BASELINE — the foundational target layer the
// AWS->DO cutover lands on — is valid, plannable HCL (plan-only; NEVER applied).
// The baseline descends to: DOKS clusters (the 6-service compute substrate), two
// Managed Postgres clusters (PG17, keycloak-db 100 GB + pyx-main-db 80 GB),
// Spaces object-storage, a DO load-balancer (+ DOKS ingress) and the account
// VPC + firewall. The string round-trips below assert the coverage matrix; the
// terraform-exec test actually runs `init && validate` when a binary is on PATH.

// wantDOBaselineResources is the coverage matrix asserted at the string level:
// the foundational baseline piece -> its concrete DigitalOcean resource.
var wantDOBaselineResources = []struct{ component, resource string }{
	// Compute substrate: the 6 platform services -> droplet_autoscale pools.
	{"platform SSO scale-group", `resource "digitalocean_droplet_autoscale" "sso"`},
	{"platform VPN scale-group", `resource "digitalocean_droplet_autoscale" "vpn"`},
	{"platform observability scale-group", `resource "digitalocean_droplet_autoscale" "obs"`},
	{"platform SAST scale-group", `resource "digitalocean_droplet_autoscale" "sast"`},
	{"platform backend scale-group", `resource "digitalocean_droplet_autoscale" "backend"`},
	{"platform mcp scale-group", `resource "digitalocean_droplet_autoscale" "mcp"`},
	// Two Managed Postgres clusters (PG17).
	{"keycloak-db (Managed PG)", `resource "digitalocean_database_cluster" "keycloak-db"`},
	{"pyx-main-db (Managed PG)", `resource "digitalocean_database_cluster" "pyx-main-db"`},
	// Object-storage baseline (Spaces).
	{"object-storage (Spaces)", `resource "digitalocean_spaces_bucket" "assets"`},
	// Shared-ALB replacement: DO LB forwarding to the backend pool by droplet tag.
	{"load-balancer", `resource "digitalocean_loadbalancer" "edge-lb"`},
	{"load-balancer droplet-tag target", `droplet_tag = "pyx-backend"`},
	// Network / account foundation.
	{"network (VPC)", `resource "digitalocean_vpc" "passo-do-baseline-net"`},
	// The DO firewall is split one-per-service (max 5 tags per firewall; 6 services).
	{"security-group (firewall)", `resource "digitalocean_firewall" "passo-do-baseline-sg_backend"`},
}

// TestDOBaselineAssembles is the plan-only round-trip proof at the string level:
// the DO account baseline descends to valid DigitalOcean HCL with the provider
// sources pinned, the two PG clusters pinned to version 17, and no AWS leakage.
func TestDOBaselineAssembles(t *testing.T) {
	t.Parallel()
	docs, err := AssembleHCL(context.Background(), MustEmbedded(),
		DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30"))
	if err != nil {
		t.Fatalf("AssembleHCL DO baseline: %v", err)
	}
	all := strings.Join(docs, "\n")

	// Provider source pinned (DO is a non-default namespace; required for
	// `terraform init`). The baseline no longer needs the kubernetes provider: the
	// scale-groups are droplet_autoscale pools and the LB forwards by droplet tag,
	// so no kubernetes_manifest resource is emitted.
	for _, want := range []string{
		`source = "digitalocean/digitalocean"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO baseline missing provider pin %q", want)
		}
	}
	// The droplet_autoscale pivot means the baseline emits no Kubernetes resources.
	for _, bad := range []string{"digitalocean_kubernetes_cluster", "kubernetes_manifest", `source = "hashicorp/kubernetes"`} {
		if strings.Contains(all, bad) {
			t.Errorf("DO baseline must not emit %q (droplet_autoscale + LB-by-tag, no DOKS)", bad)
		}
	}

	// Every baseline piece -> its concrete DO resource (the coverage matrix).
	for _, w := range wantDOBaselineResources {
		if !strings.Contains(all, w.resource) {
			t.Errorf("baseline piece %q did not render %q\n---\n%s", w.component, w.resource, all)
		}
	}

	// Both Managed Postgres clusters are pinned to PG17 (engine=pg, version=17).
	if n := strings.Count(all, `engine     = "pg"`); n != 2 {
		t.Errorf("want 2 pg managed clusters, got %d", n)
	}
	if n := strings.Count(all, `version    = "17"`); n != 2 {
		t.Errorf("want 2 PG17-pinned managed clusters, got %d", n)
	}

	// No AWS resource may leak into a DigitalOcean render.
	for _, bad := range []string{"aws_autoscaling_group", "aws_launch_template", "aws_db_instance", "aws_s3_bucket", "aws_lb"} {
		if strings.Contains(all, bad) {
			t.Errorf("DO baseline must not emit AWS resource %q", bad)
		}
	}
}

// TestDOBaselineTerraformValidate is the executable plan-only proof: the rendered
// DO baseline passes `terraform init && terraform validate`. Apply is out of
// scope — no DIGITALOCEAN_TOKEN is needed for validate. Skipped automatically
// when no terraform binary is on PATH (so `go test ./...` stays green in a
// binary-less CI); the string round-trips above still prove the render. Set
// PYX_TF_VALIDATE=0 to force-skip.
func TestDOBaselineTerraformValidate(t *testing.T) {
	if os.Getenv("PYX_TF_VALIDATE") == "0" {
		t.Skip("PYX_TF_VALIDATE=0: terraform validate explicitly disabled")
	}
	execPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform binary not on PATH: string round-trips above prove the render; install terraform to run init/validate")
	}

	docs, err := AssembleHCL(context.Background(), MustEmbedded(),
		DOBaselineInput("Frankfurt", "x86_64", "ubuntu", "1.30"))
	if err != nil {
		t.Fatalf("AssembleHCL DO baseline: %v", err)
	}

	dir := t.TempDir()
	for i, d := range docs {
		name := filepath.Join(dir, fmt.Sprintf("pyx_%03d.tf", i))
		if werr := os.WriteFile(name, []byte(d), 0o644); werr != nil {
			t.Fatalf("write doc %d: %v", i, werr)
		}
	}

	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		t.Fatalf("tfexec: %v", err)
	}
	ctx := context.Background()
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		t.Fatalf("terraform init failed — the rendered baseline is not initialisable: %v", err)
	}
	vout, verr := tf.Validate(ctx)
	if verr != nil {
		t.Fatalf("terraform validate failed — the rendered baseline is not valid DO HCL: %v", verr)
	}
	if !vout.Valid {
		t.Fatalf("terraform validate reported the baseline INVALID: %d diagnostics", vout.ErrorCount)
	}
	t.Log("terraform init && validate: GREEN — DO account baseline is valid, plannable HCL (plan-only)")
}
