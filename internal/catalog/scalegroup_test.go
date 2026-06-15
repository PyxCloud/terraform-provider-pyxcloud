package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTranslateScaleGroupAWS(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	// Dublin -> eu-west-1; 2 vCPU / 4 GiB x86_64 -> t3.medium (reused VM SKU).
	plan, err := TranslateScaleGroup(context.Background(), cat, ScaleGroupSpec{
		Name:          "web",
		Region:        "Dublin",
		Provider:      "aws",
		Architecture:  "x86_64",
		CPU:           2,
		RAM:           4,
		OS:            "ubuntu",
		Min:           2,
		Max:           6,
		Desired:       3,
		Health:        "elb",
		Network:       "production",
		Subnets:       []string{"production-subnet-1", "production-subnet-2", "production-subnet-3"},
		SecurityGroup: "production-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-west-1" {
		t.Errorf("csp_region = %q, want eu-west-1", plan.CSPRegion)
	}
	if plan.InstanceType != "t3.medium" {
		t.Errorf("instance_type = %q, want t3.medium (reused VM SKU resolution)", plan.InstanceType)
	}
	if plan.ResourceType != "aws_autoscaling_group" {
		t.Errorf("resource_type = %q, want aws_autoscaling_group", plan.ResourceType)
	}
	if plan.Min != 2 || plan.Max != 6 || plan.Desired != 3 {
		t.Errorf("bounds = %d/%d/%d, want 2/6/3", plan.Min, plan.Max, plan.Desired)
	}
	if plan.Health != HealthELB {
		t.Errorf("health = %q, want elb", plan.Health)
	}
	if !strings.HasPrefix(plan.Image, "ami-") {
		t.Errorf("aws image should be an AMI id, got %q", plan.Image)
	}
	// Multi-AZ: three subnets -> three distinct zones.
	wantZones := []string{"eu-west-1a", "eu-west-1b", "eu-west-1c"}
	if len(plan.Zones) != 3 {
		t.Fatalf("want 3 zones for 3 subnets, got %v", plan.Zones)
	}
	for i, z := range wantZones {
		if plan.Zones[i] != z {
			t.Errorf("zone[%d] = %q, want %q", i, plan.Zones[i], z)
		}
	}
	if len(plan.SubnetNames) != 3 {
		t.Errorf("want 3 subnet names spread, got %v", plan.SubnetNames)
	}
}

func TestTranslateScaleGroupGCP(t *testing.T) {
	t.Parallel()
	// Frankfurt -> europe-west3; 2/4 x86_64 -> e2-medium.
	plan, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Name: "api", Region: "Frankfurt", Provider: "gcp",
		Architecture: "x86_64", CPU: 2, RAM: 4, OS: "debian",
		Min: 1, Max: 4, Desired: 2,
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
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
	if plan.ResourceType != "google_compute_region_instance_group_manager" {
		t.Errorf("resource_type = %q, want google_compute_region_instance_group_manager", plan.ResourceType)
	}
	if plan.Min != 1 || plan.Max != 4 || plan.Desired != 2 {
		t.Errorf("bounds = %d/%d/%d, want 1/4/2", plan.Min, plan.Max, plan.Desired)
	}
	// GCP zones from europe-west3.
	wantZones := []string{"europe-west3-a", "europe-west3-b"}
	if len(plan.Zones) != 2 {
		t.Fatalf("want 2 zones, got %v", plan.Zones)
	}
	for i, z := range wantZones {
		if plan.Zones[i] != z {
			t.Errorf("zone[%d] = %q, want %q", i, plan.Zones[i], z)
		}
	}
}

// TestScaleGroupDOIsUnsupported asserts DigitalOcean returns a clean plan-time
// error (no native VM ASG primitive) rather than inventing a resource.
func TestScaleGroupDOIsUnsupported(t *testing.T) {
	t.Parallel()
	_, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Name: "box", Region: "Frankfurt", Provider: "digitalocean",
		Architecture: "x86_64", CPU: 2, RAM: 4, OS: "ubuntu",
		Min: 1, Max: 3, Network: "production",
	})
	if err == nil {
		t.Fatal("expected unsupported error for DigitalOcean autoscaling, got nil")
	}
	var unsup ErrAutoscaleUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("expected ErrAutoscaleUnsupported, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "managed-kubernetes") {
		t.Errorf("DO error should point to managed-kubernetes, got %v", err)
	}
}

// TestScaleGroupDORenderRejected asserts the renderer also refuses DO, defence
// in depth for a hand-built plan.
func TestScaleGroupDORenderRejected(t *testing.T) {
	t.Parallel()
	_, err := RenderScaleGroupHCL(ScaleGroupPlan{Provider: ProviderDigitalOcean})
	if err == nil {
		t.Fatal("expected render error for DigitalOcean scale group, got nil")
	}
}

// TestScaleGroupBoundsDefaults asserts the min/max/desired defaulting.
func TestScaleGroupBoundsDefaults(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name                    string
		min, max, desired       int
		wantMin, wantMax, wantD int
	}{
		{"all set", 2, 5, 3, 2, 5, 3},
		{"desired defaults to min", 2, 5, 0, 2, 5, 2},
		{"max defaults to min", 3, 0, 0, 3, 3, 3},
		{"zero min one max one desired", 0, 1, 0, 0, 1, 0},
		{"only min set", 2, 0, 0, 2, 2, 2},
	}
	for _, c := range cases {
		plan, err := TranslateScaleGroup(context.Background(), cat, ScaleGroupSpec{
			Region: "Dublin", Provider: "aws", Architecture: "x86_64",
			CPU: 2, RAM: 4, OS: "ubuntu",
			Min: c.min, Max: c.max, Desired: c.desired,
		})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if plan.Min != c.wantMin || plan.Max != c.wantMax || plan.Desired != c.wantD {
			t.Errorf("%s: bounds = %d/%d/%d, want %d/%d/%d",
				c.name, plan.Min, plan.Max, plan.Desired, c.wantMin, c.wantMax, c.wantD)
		}
	}
}

// TestScaleGroupHealthDefaultsAndAliases asserts health defaulting + alias map.
func TestScaleGroupHealthDefaultsAndAliases(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct{ in, want string }{
		{"", HealthEC2},
		{"ec2", HealthEC2},
		{"vm", HealthEC2},
		{"instance", HealthEC2},
		{"elb", HealthELB},
		{"lb", HealthELB},
		{"load-balancer", HealthELB},
	}
	for _, c := range cases {
		plan, err := TranslateScaleGroup(context.Background(), cat, ScaleGroupSpec{
			Region: "Dublin", Provider: "aws", CPU: 2, RAM: 4, Min: 1, Max: 2, Health: c.in,
		})
		if err != nil {
			t.Fatalf("health %q: %v", c.in, err)
		}
		if plan.Health != c.want {
			t.Errorf("health %q -> %q, want %q", c.in, plan.Health, c.want)
		}
	}
}

// TestScaleGroupSKUNoMatchIsHardError asserts the reused SKU resolution still
// hard-errors (never a silent fallback) for the scale group.
func TestScaleGroupSKUNoMatchIsHardError(t *testing.T) {
	t.Parallel()
	_, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Region: "Dublin", Provider: "aws", Architecture: "x86_64",
		CPU: 999, RAM: 9999, OS: "ubuntu", Min: 1, Max: 2,
	})
	if err == nil {
		t.Fatal("expected SKU no-match error, got nil")
	}
	var notFound ErrSKUNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("expected ErrSKUNotFound, got %T: %v", err, err)
	}
}

func TestScaleGroupValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name string
		spec ScaleGroupSpec
	}{
		{"missing region", ScaleGroupSpec{Provider: "aws", CPU: 2, RAM: 4, Min: 1, Max: 2}},
		{"missing provider", ScaleGroupSpec{Region: "Dublin", CPU: 2, RAM: 4, Min: 1, Max: 2}},
		{"unknown provider", ScaleGroupSpec{Region: "Dublin", Provider: "azure", CPU: 2, RAM: 4, Min: 1, Max: 2}},
		{"bad architecture", ScaleGroupSpec{Region: "Dublin", Provider: "aws", Architecture: "riscv", CPU: 2, RAM: 4, Min: 1, Max: 2}},
		{"bad os", ScaleGroupSpec{Region: "Dublin", Provider: "aws", OS: "windows", CPU: 2, RAM: 4, Min: 1, Max: 2}},
		{"cpu < 1", ScaleGroupSpec{Region: "Dublin", Provider: "aws", CPU: 0, RAM: 4, Min: 1, Max: 2}},
		{"ram < 1", ScaleGroupSpec{Region: "Dublin", Provider: "aws", CPU: 2, RAM: 0, Min: 1, Max: 2}},
		{"negative min", ScaleGroupSpec{Region: "Dublin", Provider: "aws", CPU: 2, RAM: 4, Min: -1, Max: 2}},
		{"max below min", ScaleGroupSpec{Region: "Dublin", Provider: "aws", CPU: 2, RAM: 4, Min: 5, Max: 2}},
		{"desired below min", ScaleGroupSpec{Region: "Dublin", Provider: "aws", CPU: 2, RAM: 4, Min: 3, Max: 5, Desired: 1}},
		{"desired above max", ScaleGroupSpec{Region: "Dublin", Provider: "aws", CPU: 2, RAM: 4, Min: 1, Max: 3, Desired: 5}},
		{"bad health", ScaleGroupSpec{Region: "Dublin", Provider: "aws", CPU: 2, RAM: 4, Min: 1, Max: 2, Health: "ping"}},
	}
	for _, c := range cases {
		if _, err := TranslateScaleGroup(context.Background(), cat, c.spec); err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
		}
	}
}

// TestRenderScaleGroupAWS asserts the per-provider shaping of the rendered HCL:
// launch template + ASG, multi-AZ vpc_zone_identifier, min/max/desired wiring,
// health-check type, rolling refresh.
func TestRenderScaleGroupAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Name: "web", Region: "Dublin", Provider: "aws", Architecture: "x86_64",
		CPU: 2, RAM: 4, OS: "ubuntu", Min: 2, Max: 6, Desired: 3, Health: "elb",
		Network:       "production",
		Subnets:       []string{"production-subnet-1", "production-subnet-2", "production-subnet-3"},
		SecurityGroup: "production-web",
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderScaleGroupHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_launch_template" "web_lt"`,
		`resource "aws_autoscaling_group" "web_asg"`,
		`image_id      = "ami-`,
		`instance_type = "t3.medium"`,
		`vpc_security_group_ids = [aws_security_group.production-web.id]`,
		`min_size            = 2`,
		`max_size            = 6`,
		`desired_capacity    = 3`,
		`health_check_type   = "ELB"`,
		`vpc_zone_identifier = [aws_subnet.production_1.id, aws_subnet.production_2.id, aws_subnet.production_3.id]`,
		`id      = aws_launch_template.web_lt.id`,
		`instance_refresh {`,
		`strategy = "Rolling"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws ASG HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL is not ASCII:\n%s", hcl)
	}
}

func TestRenderScaleGroupAWSEC2Health(t *testing.T) {
	t.Parallel()
	plan, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Name: "worker", Region: "Dublin", Provider: "aws",
		CPU: 2, RAM: 4, Min: 1, Max: 3, // default health -> ec2
		Network: "production", Subnets: []string{"production-subnet-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderScaleGroupHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hcl, `health_check_type   = "EC2"`) {
		t.Errorf("default health should render EC2 health check:\n%s", hcl)
	}
}

func TestRenderScaleGroupGCP(t *testing.T) {
	t.Parallel()
	plan, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Name: "api", Region: "Frankfurt", Provider: "gcp", Architecture: "x86_64",
		CPU: 2, RAM: 4, OS: "debian", Min: 1, Max: 4, Desired: 2, Health: "elb",
		Network: "production", Subnets: []string{"production-subnet-1", "production-subnet-2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderScaleGroupHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "google_compute_instance_template" "api_tmpl"`,
		`resource "google_compute_region_instance_group_manager" "api_mig"`,
		`resource "google_compute_region_autoscaler" "api_as"`,
		`machine_type = "e2-medium"`,
		`source_image = "`,
		`region                    = "europe-west3"`,
		`instance_template = google_compute_instance_template.api_tmpl.id`,
		`min_replicas = 1`,
		`max_replicas = 4`,
		`auto_healing_policies {`,
		`update_policy {`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("gcp ASG HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestRenderScaleGroupGCPNoAutohealOnEC2 asserts the autohealing policy is only
// emitted for elb/lb health (instance-only health does not autoheal via HC).
func TestRenderScaleGroupGCPNoAutohealOnEC2(t *testing.T) {
	t.Parallel()
	plan, err := TranslateScaleGroup(context.Background(), MustEmbedded(), ScaleGroupSpec{
		Name: "api", Region: "Frankfurt", Provider: "gcp",
		CPU: 2, RAM: 4, Min: 1, Max: 2, // default ec2
		Network: "production", Subnets: []string{"production-subnet-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderScaleGroupHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hcl, "auto_healing_policies") {
		t.Errorf("ec2 health should not emit auto_healing_policies:\n%s", hcl)
	}
}
