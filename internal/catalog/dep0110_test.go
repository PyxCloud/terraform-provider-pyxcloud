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

// DEP-01.10: placement="vm" explicitly self-hosts managed-database (PostgreSQL)
// and cache (Redis) on a VM even on providers that have a native managed
// service — no managed cluster is rendered, the service runs from the VM's
// user_data. Default (no placement) keeps the native managed cluster.

func TestAssembleHCLExplicitVMPlacement(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}

	tests := []struct {
		name        string
		provider    string
		region      string
		component   AssembleComponent
		absent      string // must NOT appear
		wantContain []string
	}{
		{
			name:      "digitalocean cache placement vm renders droplet redis",
			provider:  ProviderDigitalOcean,
			region:    "New York",
			component: AssembleComponent{Name: "sessions", Type: "cache", Placement: "vm", Cache: &AssembleCache{Engine: "redis", MemoryGB: 1}},
			absent:    "digitalocean_database_cluster",
			wantContain: []string{
				"# pyxcloud vm placement:",
				"resource \"digitalocean_droplet\"",
				"redis:7",
				"user_data = <<-PYXUSERDATA",
				"docker run -d --restart=always",
			},
		},
		{
			name:      "digitalocean managed-database placement vm renders droplet postgres",
			provider:  ProviderDigitalOcean,
			region:    "New York",
			component: AssembleComponent{Name: "main-db", Type: "managed-database", Placement: "vm", MDB: &AssembleMDB{Engine: "postgres", CPU: 1, RAM: 1}},
			absent:    "digitalocean_database_cluster",
			wantContain: []string{
				"# pyxcloud vm placement:",
				"resource \"digitalocean_droplet\"",
				"postgres:16",
				"docker run -d --restart=always",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
				Name: "demo", Provider: tc.provider, Region: tc.region,
				Components: []AssembleComponent{tc.component},
			})
			if err != nil {
				t.Fatalf("AssembleHCL: %v", err)
			}
			all := strings.Join(docs, "\n")
			if strings.Contains(all, tc.absent) {
				t.Fatalf("managed cluster still rendered: %q\n---\n%s", tc.absent, all)
			}
			for _, want := range tc.wantContain {
				if !strings.Contains(all, want) {
					t.Fatalf("VM placement HCL missing %q\n---\n%s", want, all)
				}
			}
		})
	}
}

func TestAssembleHCLDefaultPlacementKeepsManagedCluster(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: ProviderDigitalOcean, Region: "New York",
		Components: []AssembleComponent{
			{Name: "sessions", Type: "cache", Cache: &AssembleCache{Engine: "redis"}},
			{Name: "main-db", Type: "managed-database", MDB: &AssembleMDB{Engine: "postgres", CPU: 1, RAM: 1}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	all := strings.Join(docs, "\n")
	if n := strings.Count(all, "digitalocean_database_cluster"); n < 2 {
		t.Fatalf("expected both managed clusters, got %d occurrences\n---\n%s", n, all)
	}
	if strings.Contains(all, "resource \"digitalocean_droplet\"") {
		t.Fatalf("VM rendered without placement=vm\n---\n%s", all)
	}
}

func TestDep0110TerraformValidateVMPlacement(t *testing.T) {
	if os.Getenv("PYX_TF_VALIDATE") == "0" {
		t.Skip("PYX_TF_VALIDATE=0: terraform validate explicitly disabled")
	}
	execPath, err := exec.LookPath("terraform")
	if err != nil {
		t.Skip("terraform binary not on PATH: string round-trips above prove the render")
	}
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
		Name: "vmplacement", Provider: ProviderDigitalOcean, Region: "New York",
		Components: []AssembleComponent{
			{Name: "sessions", Type: "cache", Placement: "vm", Cache: &AssembleCache{Engine: "redis", MemoryGB: 1}},
			{Name: "main-db", Type: "managed-database", Placement: "vm", MDB: &AssembleMDB{Engine: "postgres", CPU: 1, RAM: 1}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	dir := t.TempDir()
	for i, d := range docs {
		if werr := os.WriteFile(filepath.Join(dir, fmt.Sprintf("pyx_%03d.tf", i)), []byte(d), 0o644); werr != nil {
			t.Fatalf("write doc %d: %v", i, werr)
		}
	}
	tf, err := tfexec.NewTerraform(dir, execPath)
	if err != nil {
		t.Fatalf("tfexec: %v", err)
	}
	ctx := context.Background()
	if err := tf.Init(ctx, tfexec.Upgrade(false)); err != nil {
		t.Fatalf("terraform init failed: %v", err)
	}
	vout, verr := tf.Validate(ctx)
	if verr != nil {
		t.Fatalf("terraform validate failed: %v", verr)
	}
	if !vout.Valid {
		t.Fatalf("terraform validate reported INVALID: %d diagnostics", vout.ErrorCount)
	}
	t.Log("terraform init && validate: GREEN — VM-placement output (no managed clusters) is valid DO HCL")
}

func TestAssembleHCLPlacementValidation(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	tests := []struct {
		name      string
		component AssembleComponent
		wantErr   string
	}{
		{
			name:      "unknown placement value rejected",
			component: AssembleComponent{Name: "sessions", Type: "cache", Placement: "on-prem", Cache: &AssembleCache{}},
			wantErr:   "unknown placement",
		},
		{
			name:      "placement vm on other type rejected",
			component: AssembleComponent{Name: "assets", Type: "object-storage", Placement: "vm", ObjectStorage: &AssembleObjectStorage{}},
			wantErr:   "not supported for this component type",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AssembleHCL(context.Background(), cat, AssembleInput{
				Name: "demo", Provider: ProviderDigitalOcean, Region: "New York",
				Components: []AssembleComponent{tc.component},
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}
