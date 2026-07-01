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

// prod_estate_test.go — pd-MIG-CUTOVER-F0-03 (EPIC-AWS-TO-DO-MIGRATION).
//
// The full-prod-estate cutover proof: the canonical whole-of-prod topology
// (prod_estate.go) renders BOTH ways —
//   - AWS: the SOURCE estate, everything as it runs today (incl. the AWS-only
//     bespoke components SES + frontends), producing native aws_* resources.
//   - DO:  the TARGET estate, the same topology minus the documented bespoke gaps
//     (docs/cutover/BESPOKE-GAPS.md), producing native digitalocean_* / operator
//     resources — and, when a terraform binary is on PATH, passing
//     `terraform init && validate` (plan-only, GREEN).

// TestProdEstateAssemblesForAWS proves the SOURCE (AWS) estate descends to native
// AWS resources, including the AWS-only bespoke components.
func TestProdEstateAssemblesForAWS(t *testing.T) {
	t.Parallel()
	docs, err := AssembleHCL(context.Background(), MustEmbedded(),
		ProdEstateInput("aws", "Dublin", "x86_64", "ubuntu", ""))
	if err != nil {
		t.Fatalf("AssembleHCL prod estate (AWS): %v", err)
	}
	all := strings.Join(docs, "\n")

	// The 6 platform services -> 6 AWS autoscaling groups.
	if n := strings.Count(all, `resource "aws_autoscaling_group"`); n != 6 {
		t.Errorf("want 6 aws_autoscaling_group, got %d", n)
	}
	// The two production Managed Postgres clusters.
	if n := strings.Count(all, `resource "aws_db_instance"`); n != 2 {
		t.Errorf("want 2 aws_db_instance, got %d", n)
	}
	// The ~18 production buckets -> aws_s3_bucket.
	if n := strings.Count(all, `resource "aws_s3_bucket" "`); n != len(prodBuckets) {
		t.Errorf("want %d aws_s3_bucket, got %d", len(prodBuckets), n)
	}
	for _, want := range []string{
		`resource "aws_ecr_repository"`,          // container-registry
		`resource "aws_dynamodb_table"`,          // key-value-store (JIT allowlist)
		`resource "aws_lb"`,                      // edge L7 load-balancer
		`resource "aws_secretsmanager_secret"`,   // secrets-manager native on AWS
		`resource "aws_acm_certificate"`,         // tls-certificate native on AWS
		`resource "aws_cloudwatch_metric_alarm"`, // monitoring: CloudWatch+SNS on AWS
		`resource "aws_sqs_queue"`,               // prod queue native on AWS
		`resource "aws_eip"`,                     // reserved-ip (VPN endpoint)
		`resource "aws_ses_domain_identity"`,     // BESPOKE-GAP: SES (AWS-only)
		`resource "aws_amplify_app" "marketing"`, // GAP-1 closed: frontends -> Amplify on AWS
		`resource "aws_amplify_app" "console"`,
		`resource "aws_amplify_app" "vibe"`,
		`resource "aws_amplify_branch"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("AWS prod estate missing %q", want)
		}
	}
	// The 3 frontends -> 3 Amplify apps.
	if n := strings.Count(all, `resource "aws_amplify_app"`); n != len(prodStaticSites) {
		t.Errorf("want %d aws_amplify_app, got %d", len(prodStaticSites), n)
	}
}

// TestProdEstateAssemblesForDO proves the TARGET (DigitalOcean) estate descends to
// native DO / operator resources, with the AWS-only bespoke components excluded
// (documented gaps) and no AWS leakage.
func TestProdEstateAssemblesForDO(t *testing.T) {
	t.Parallel()
	docs, err := AssembleHCL(context.Background(), MustEmbedded(),
		ProdEstateInput("digitalocean", "Frankfurt", "x86_64", "ubuntu", "1.30"))
	if err != nil {
		t.Fatalf("AssembleHCL prod estate (DO): %v", err)
	}
	all := strings.Join(docs, "\n")

	// Provider sources pinned (non-default namespaces).
	for _, want := range []string{
		`source = "digitalocean/digitalocean"`,
		`source = "hashicorp/kubernetes"`,
		`source = "hashicorp/helm"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO prod estate missing provider pin %q", want)
		}
	}

	// The 6 platform services -> 6 DOKS clusters.
	if n := strings.Count(all, `resource "digitalocean_kubernetes_cluster"`); n != 6 {
		t.Errorf("want 6 digitalocean_kubernetes_cluster, got %d", n)
	}
	// The two Managed Postgres -> digitalocean_database_cluster (+ JIT Redis = 3 total).
	if n := strings.Count(all, `resource "digitalocean_database_cluster"`); n != 3 {
		t.Errorf("want 3 digitalocean_database_cluster (2 pg + 1 redis), got %d", n)
	}
	// The ~18 production buckets PLUS the 3 static-site origins -> digitalocean_spaces_bucket.
	if n, want := strings.Count(all, `resource "digitalocean_spaces_bucket" "`), len(prodBuckets)+len(prodStaticSites); n != want {
		t.Errorf("want %d digitalocean_spaces_bucket (%d buckets + %d static sites), got %d",
			want, len(prodBuckets), len(prodStaticSites), n)
	}
	// GAP-1 closed: the 3 frontends descend to Spaces static website + Cloudflare CDN.
	for _, want := range []string{
		`resource "digitalocean_spaces_bucket" "marketing"`,
		`static-website origin: index="index.html"`,      // Spaces static website (comment; served via CDN)
		`resource "cloudflare_dns_record" "console-cdn"`, // Cloudflare CDN front (proxied CNAME)
		`resource "cloudflare_zone_setting" "vibe-cdn-always_online"`,
		`digitaloceanspaces.com`, // CDN origin = the Spaces website endpoint
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO prod estate (static-site) missing %q", want)
		}
	}
	// The static-site CDN pins the Cloudflare provider.
	if !strings.Contains(all, `source = "cloudflare/cloudflare"`) {
		t.Errorf("DO prod estate missing Cloudflare provider pin (static-site CDN)")
	}
	for _, want := range []string{
		`resource "digitalocean_container_registry" "app-images"`,
		`resource "digitalocean_loadbalancer" "edge-lb"`,
		`resource "kubernetes_manifest" "edge-lb_ingress"`,
		`resource "helm_release" "app-secrets_operator"`, // Vault-HA operator (secrets)
		`resource "kubernetes_cron_job_v1" "nightly"`,    // scheduled-trigger
		`resource "digitalocean_reserved_ip" "vpn-endpoint"`,
		`resource "digitalocean_vpc" "passo-prod-net"`,
		`resource "digitalocean_firewall" "passo-prod-sg"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO prod estate missing %q", want)
		}
	}

	// The AWS-only bespoke gaps must NOT appear in a DO render.
	for _, bad := range []string{
		"aws_autoscaling_group", "aws_s3_bucket", "aws_acm_certificate",
		"aws_ses_domain_identity", // SES is a documented gap, excluded from DO
		"aws_amplify_app",         // frontends are a documented gap
	} {
		if strings.Contains(all, bad) {
			t.Errorf("DO prod estate must not emit AWS resource %q", bad)
		}
	}
}

// TestProdEstateTerraformValidate is the executable plan-only proof: BOTH the AWS
// and DO renders pass `terraform init && terraform validate` (GREEN). Skipped when
// no terraform binary is on PATH; set PYX_TF_VALIDATE=0 to force-skip.
func TestProdEstateTerraformValidate(t *testing.T) {
	if os.Getenv("PYX_TF_VALIDATE") == "0" {
		t.Skip("PYX_TF_VALIDATE=0: terraform validate explicitly disabled")
	}
	execPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform binary not on PATH: string round-trips above prove the render")
	}

	cases := []struct {
		name     string
		provider string
		region   string
		k8s      string
	}{
		{"aws", "aws", "Dublin", ""},
		{"do", "digitalocean", "Frankfurt", "1.30"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			docs, err := AssembleHCL(context.Background(), MustEmbedded(),
				ProdEstateInput(tc.provider, tc.region, "x86_64", "ubuntu", tc.k8s))
			if err != nil {
				t.Fatalf("AssembleHCL prod estate (%s): %v", tc.provider, err)
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
				t.Fatalf("terraform init failed (%s): %v", tc.provider, err)
			}
			vout, verr := tf.Validate(ctx)
			if verr != nil {
				t.Fatalf("terraform validate failed (%s): %v", tc.provider, verr)
			}
			if !vout.Valid {
				t.Fatalf("terraform validate reported %s estate INVALID: %d diagnostics", tc.provider, vout.ErrorCount)
			}
			t.Logf("terraform init && validate: GREEN — full %s prod estate is valid, plannable HCL", tc.provider)
		})
	}
}
