package catalog

import "testing"

func TestTranslateIAMAWSFull(t *testing.T) {
	plan, err := TranslateIAM(IAMSpec{
		Name:     "keycloak",
		Provider: "aws",
		InlinePolicies: []IAMPolicyDoc{
			{Name: "s3-read", Document: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject"],"Resource":"*"}]}`},
		},
		ManagedPolicyARNs: []string{"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"},
		InstanceProfile:   true,
	})
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	if plan.AssumeService != "ec2.amazonaws.com" {
		t.Errorf("default assume service = %q", plan.AssumeService)
	}
	if plan.InstanceProfileName != "keycloak" {
		t.Errorf("instance profile name = %q", plan.InstanceProfileName)
	}
	hcl, err := RenderIAMHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_iam_role\" \"keycloak\"",
		"assume_role_policy = jsonencode(",
		"Service = \"ec2.amazonaws.com\"",
		"resource \"aws_iam_role_policy\" \"keycloak-s3-read\"",
		"s3:GetObject",
		"resource \"aws_iam_role_policy_attachment\" \"keycloak-managed-0\"",
		"AmazonSSMManagedInstanceCore",
		"resource \"aws_iam_instance_profile\" \"keycloak\"",
	} {
		if !contains(hcl, want) {
			t.Errorf("AWS IAM HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestTranslateIAMValidation(t *testing.T) {
	if _, err := TranslateIAM(IAMSpec{Provider: "aws"}); err == nil {
		t.Error("expected error: name required")
	}
	if _, err := TranslateIAM(IAMSpec{Name: "r", Provider: "aws",
		InlinePolicies: []IAMPolicyDoc{{Name: "p"}}}); err == nil {
		t.Error("expected error: inline policy needs a document")
	}
}

func TestTranslateIAMGCPRefusesAWSPolicies(t *testing.T) {
	_, err := TranslateIAM(IAMSpec{Name: "svc", Provider: "gcp",
		InlinePolicies: []IAMPolicyDoc{{Name: "p", Document: "{}"}}})
	if err == nil {
		t.Error("GCP must refuse AWS-style inline policies (no silent drop)")
	}
	// A policy-free GCP service account is fine.
	plan, err := TranslateIAM(IAMSpec{Name: "svc", Provider: "gcp"})
	if err != nil {
		t.Fatalf("gcp SA: %v", err)
	}
	hcl, _ := RenderIAMHCL(plan)
	if !contains(hcl, "google_service_account") {
		t.Errorf("gcp render missing service account:\n%s", hcl)
	}
}

func TestTranslateIAMUnsupportedProvider(t *testing.T) {
	if _, err := TranslateIAM(IAMSpec{Name: "r", Provider: "digitalocean"}); err == nil {
		t.Error("DO has no IAM → expected hard unsupported error")
	}
}
