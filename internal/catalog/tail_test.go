package catalog

import "testing"

func TestTranslatePrefixListAWS(t *testing.T) {
	plan, err := TranslatePrefixList(PrefixListSpec{
		Name:     "office-egress",
		Provider: "aws",
		Entries:  []string{"87.120.111.232/32", "212.24.22.193/32"},
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if plan.MaxEntries != 2 || plan.AddressFam != "IPv4" {
		t.Errorf("defaults wrong: %+v", plan)
	}
	hcl, err := RenderPrefixListHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_ec2_managed_prefix_list\"",
		"max_entries    = 2",
		"cidr = \"87.120.111.232/32\"",
	} {
		if !contains(hcl, want) {
			t.Errorf("prefix-list HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestPrefixListValidation(t *testing.T) {
	if _, err := TranslatePrefixList(PrefixListSpec{Provider: "aws", Entries: []string{"1.2.3.4/32"}}); err == nil {
		t.Error("expected error: name required")
	}
	if _, err := TranslatePrefixList(PrefixListSpec{Name: "n", Provider: "aws"}); err == nil {
		t.Error("expected error: need an entry")
	}
	if _, err := TranslatePrefixList(PrefixListSpec{Name: "n", Provider: "gcp", Entries: []string{"1.2.3.4/32"}}); err == nil {
		t.Error("expected error: unsupported provider")
	}
}

func TestTranslateCanaryAWS(t *testing.T) {
	plan, err := TranslateCanary(CanarySpec{
		Name:           "login",
		Provider:       "aws",
		ArtifactBucket: "pyx-canary-artifacts",
		Schedule:       "rate(5 minutes)",
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if plan.RuntimeVersion == "" {
		t.Error("runtime should default")
	}
	hcl, err := RenderCanaryHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_synthetics_canary\"",
		"expression = \"rate(5 minutes)\"",
		"artifact_s3_location = \"s3://pyx-canary-artifacts/login\"",
	} {
		if !contains(hcl, want) {
			t.Errorf("canary HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestCanaryValidation(t *testing.T) {
	if _, err := TranslateCanary(CanarySpec{Provider: "aws", ArtifactBucket: "b", Schedule: "rate(1 minute)"}); err == nil {
		t.Error("expected error: name required")
	}
	if _, err := TranslateCanary(CanarySpec{Name: "n", Provider: "aws", Schedule: "rate(1 minute)"}); err == nil {
		t.Error("expected error: artifact_bucket required")
	}
	if _, err := TranslateCanary(CanarySpec{Name: "n", Provider: "digitalocean", ArtifactBucket: "b", Schedule: "s"}); err == nil {
		t.Error("expected error: unsupported provider")
	}
}
