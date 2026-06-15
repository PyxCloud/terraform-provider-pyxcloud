package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTranslateVMAWS(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// Dublin -> eu-west-1; 2 vCPU / 4 GiB x86_64 -> t3.medium.
	plan, err := TranslateVM(context.Background(), cat, VMSpec{
		Name:          "web",
		Region:        "Dublin",
		Provider:      "aws",
		Architecture:  "x86_64",
		CPU:           2,
		RAM:           4,
		OS:            "ubuntu",
		Count:         2,
		Network:       "production",
		Subnet:        "production-subnet-1",
		SecurityGroup: "production-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion)
	}
	if plan.InstanceType != "t3.medium" {
		t.Errorf("instance_type = %q, want t3.medium", plan.InstanceType)
	}
	if plan.ResourceType != "aws_instance" {
		t.Errorf("resource_type = %q, want aws_instance", plan.ResourceType)
	}
	if !strings.HasPrefix(plan.Image, "ami-") {
		t.Errorf("aws image should be an AMI id, got %q", plan.Image)
	}
	if len(plan.Instances) != 2 {
		t.Fatalf("want 2 instances for count=2, got %d", len(plan.Instances))
	}
	if plan.Instances[0].Name != "web-1" || plan.Instances[1].Name != "web-2" {
		t.Errorf("instance names = %v, want web-1, web-2", plan.Instances)
	}
}

func TestTranslateVMAWSArm64(t *testing.T) {
	t.Parallel()
	// arm64 2/4 -> t4g.medium (Graviton).
	plan, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Name: "edge", Region: "Dublin", Provider: "aws",
		Architecture: "arm64", CPU: 2, RAM: 4, OS: "ubuntu", Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.InstanceType != "t4g.medium" {
		t.Errorf("instance_type = %q, want t4g.medium", plan.InstanceType)
	}
	if plan.Architecture != "arm64" {
		t.Errorf("architecture = %q, want arm64", plan.Architecture)
	}
}

func TestTranslateVMGCP(t *testing.T) {
	t.Parallel()
	// Frankfurt -> europe-west3; 2/4 x86_64 -> e2-medium.
	plan, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Name: "api", Region: "Frankfurt", Provider: "gcp",
		Architecture: "x86_64", CPU: 2, RAM: 4, OS: "debian", Count: 1,
		Network: "production", Subnet: "production-subnet-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "europe-west3" {
		t.Errorf("csp_region = %q, want europe-west3", plan.CSPRegion)
	}
	if plan.InstanceType != "e2-medium" {
		t.Errorf("instance_type = %q, want e2-medium", plan.InstanceType)
	}
	if plan.ResourceType != "google_compute_instance" {
		t.Errorf("resource_type = %q, want google_compute_instance", plan.ResourceType)
	}
	// GCP image is the catalog family form (project/family).
	if !strings.Contains(plan.Image, "debian-cloud/debian-12") {
		t.Errorf("gcp debian image = %q, want a debian-cloud/debian-12 family", plan.Image)
	}
}

func TestTranslateVMDO(t *testing.T) {
	t.Parallel()
	// Frankfurt -> fra1; 2/4 x86_64 -> s-2vcpu-4gb.
	plan, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Name: "box", Region: "Frankfurt", Provider: "digitalocean",
		Architecture: "x86_64", CPU: 2, RAM: 4, OS: "ubuntu", Count: 1,
		Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSP != "do" {
		t.Errorf("csp = %q, want do", plan.CSP)
	}
	if plan.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", plan.CSPRegion)
	}
	if plan.InstanceType != "s-2vcpu-4gb" {
		t.Errorf("instance_type = %q, want s-2vcpu-4gb", plan.InstanceType)
	}
	if plan.ResourceType != "digitalocean_droplet" {
		t.Errorf("resource_type = %q, want digitalocean_droplet", plan.ResourceType)
	}
	if plan.Image != "ubuntu-24-04-x64" {
		t.Errorf("do image = %q, want ubuntu-24-04-x64", plan.Image)
	}
}

// TestVMDefaults asserts that an unset architecture/os/count default to
// x86_64 / ubuntu / 1 (the canonical wizard defaults).
func TestVMDefaults(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Region: "Dublin", Provider: "aws", CPU: 2, RAM: 1, // t3.micro
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Architecture != "x86_64" {
		t.Errorf("default architecture = %q, want x86_64", plan.Architecture)
	}
	if plan.OSName != "ubuntu" || plan.OSVersion != "24.04" {
		t.Errorf("default os = %q/%q, want ubuntu/24.04", plan.OSName, plan.OSVersion)
	}
	if len(plan.Instances) != 1 {
		t.Errorf("default count should yield 1 instance, got %d", len(plan.Instances))
	}
	if plan.InstanceType != "t3.micro" {
		t.Errorf("instance_type = %q, want t3.micro", plan.InstanceType)
	}
}

// TestVMSKUNoMatchIsHardError asserts an unsatisfiable sizing is a plan-time
// error that lists the nearest available sizes (never a silent fallback).
func TestVMSKUNoMatchIsHardError(t *testing.T) {
	t.Parallel()
	_, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Region: "Dublin", Provider: "aws", Architecture: "x86_64",
		CPU: 999, RAM: 9999, OS: "ubuntu",
	})
	if err == nil {
		t.Fatal("expected SKU no-match error for 999cpu/9999ram, got nil")
	}
	var notFound ErrSKUNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("expected ErrSKUNotFound, got %T: %v", err, err)
	}
	if len(notFound.Nearest) == 0 {
		t.Error("no-match error should list nearest available sizes")
	}
	if !strings.Contains(err.Error(), "Nearest available sizes") {
		t.Errorf("error should mention nearest sizes, got %v", err)
	}
}

// TestVMOSImageNoMatchIsHardError asserts an unavailable os/version is a hard
// plan-time error.
func TestVMOSImageNoMatchIsHardError(t *testing.T) {
	t.Parallel()
	// ubuntu 20.04 is not in the OS snapshot -> hard error.
	_, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Region: "Dublin", Provider: "aws", Architecture: "x86_64",
		CPU: 2, RAM: 4, OS: "ubuntu", OSVersion: "20.04",
	})
	if err == nil {
		t.Fatal("expected OS-image no-match error for ubuntu 20.04, got nil")
	}
	var notFound ErrOSImageNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("expected ErrOSImageNotFound, got %T: %v", err, err)
	}
}

// TestVMMissingRegionIsHardError asserts the region-resolution hard error path.
func TestVMMissingRegionIsHardError(t *testing.T) {
	t.Parallel()
	// Dublin has no DigitalOcean entry -> plan-time error.
	_, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Region: "Dublin", Provider: "digitalocean", CPU: 2, RAM: 4,
	})
	if err == nil {
		t.Fatal("expected hard error for Dublin/digitalocean, got nil")
	}
}

func TestVMValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name string
		spec VMSpec
	}{
		{"missing region", VMSpec{Provider: "aws", CPU: 2, RAM: 4}},
		{"missing provider", VMSpec{Region: "Dublin", CPU: 2, RAM: 4}},
		{"unknown provider", VMSpec{Region: "Dublin", Provider: "azure", CPU: 2, RAM: 4}},
		{"bad architecture", VMSpec{Region: "Dublin", Provider: "aws", Architecture: "riscv", CPU: 2, RAM: 4}},
		{"bad os", VMSpec{Region: "Dublin", Provider: "aws", OS: "windows", CPU: 2, RAM: 4}},
		{"cpu < 1", VMSpec{Region: "Dublin", Provider: "aws", CPU: 0, RAM: 4}},
		{"ram < 1", VMSpec{Region: "Dublin", Provider: "aws", CPU: 2, RAM: 0}},
		{"negative count", VMSpec{Region: "Dublin", Provider: "aws", CPU: 2, RAM: 4, Count: -1}},
	}
	for _, c := range cases {
		if _, err := TranslateVM(context.Background(), cat, c.spec); err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
		}
	}
}

// TestSubnetResourceLabel asserts the VM references the SAME resource label the
// network component emits for a subnet (so the generated .tf actually links).
func TestSubnetResourceLabel(t *testing.T) {
	t.Parallel()
	cases := []struct{ network, subnet, want string }{
		{"production", "production-subnet-1", "production_1"},
		{"production", "production-subnet-3", "production_3"},
		{"edge-net", "edge-net-subnet-2", "edge-net_2"}, // hyphen preserved by tfName, matching the network renderer
		{"production", "custom-name", "custom-name"},    // no -subnet- suffix -> fallback
		{"", "production-subnet-1", "production-subnet-1"},
	}
	for _, c := range cases {
		if got := subnetResourceLabel(c.network, c.subnet); got != c.want {
			t.Errorf("subnetResourceLabel(%q,%q) = %q, want %q", c.network, c.subnet, got, c.want)
		}
	}
}

// TestRenderVMAWS asserts the per-provider shaping of the rendered HCL.
func TestRenderVMAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Name: "web", Region: "Dublin", Provider: "aws", Architecture: "x86_64",
		CPU: 2, RAM: 4, OS: "ubuntu", Count: 2,
		Network: "production", Subnet: "production-subnet-1", SecurityGroup: "production-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_instance" "web-1"`,
		`resource "aws_instance" "web-2"`,
		`instance_type = "t3.medium"`,
		`subnet_id     = aws_subnet.production_1.id`,
		`vpc_security_group_ids = [aws_security_group.production-web.id]`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL is not ASCII:\n%s", hcl)
	}
}

func TestRenderVMGCP(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Name: "api", Region: "Frankfurt", Provider: "gcp", Architecture: "x86_64",
		CPU: 2, RAM: 4, OS: "debian", Count: 1,
		Network: "production", Subnet: "production-subnet-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "google_compute_instance" "api-1"`,
		`machine_type = "e2-medium"`,
		`zone         = "europe-west3-a"`,
		`subnetwork = google_compute_subnetwork.production_1.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("gcp HCL missing %q:\n%s", want, hcl)
		}
	}
}

func TestRenderVMDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateVM(context.Background(), MustEmbedded(), VMSpec{
		Name: "box", Region: "Frankfurt", Provider: "digitalocean", Architecture: "x86_64",
		CPU: 2, RAM: 4, OS: "ubuntu", Count: 1, Network: "production",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "digitalocean_droplet" "box-1"`,
		`size   = "s-2vcpu-4gb"`,
		`image  = "ubuntu-24-04-x64"`,
		`region = "fra1"`,
		`vpc_uuid = digitalocean_vpc.production.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do HCL missing %q:\n%s", want, hcl)
		}
	}
}
