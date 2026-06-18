package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestTranslateIAMAWS(t *testing.T) {
	p, err := TranslateIAM(context.Background(), nil, IAMSpec{
		Name: "app-role", Provider: "aws",
		InlinePolicies:    []IAMPolicy{{Name: "s3", Document: `{"Version":"2012-10-17","Statement":[]}`}},
		ManagedPolicyARNs: []string{"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"},
		InstanceProfile:   true,
	})
	if err != nil {
		t.Fatalf("TranslateIAM aws: %v", err)
	}
	if p.AssumeService != "ec2.amazonaws.com" || p.ResourceType != "aws_iam_role" {
		t.Errorf("aws iam plan defaults wrong: %+v", p)
	}
	hcl, err := RenderIAMHCL(p)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		"resource \"aws_iam_role\" \"app-role\"",
		"assume_role_policy = jsonencode(",
		"Service = \"ec2.amazonaws.com\"",
		"resource \"aws_iam_role_policy\" \"app-role-s3\"",
		"policy = <<-PYXIAMPOLICY",
		"resource \"aws_iam_role_policy_attachment\" \"app-role-managed-1\"",
		"policy_arn = \"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore\"",
		"resource \"aws_iam_instance_profile\" \"app-role\"",
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("AWS IAM HCL missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestTranslateIAMDOUnsupported(t *testing.T) {
	_, err := TranslateIAM(context.Background(), nil, IAMSpec{Name: "r", Provider: "digitalocean"})
	if err == nil || !strings.Contains(err.Error(), "unsupported on digitalocean") {
		t.Errorf("expected DO unsupported error, got %v", err)
	}
}

func TestTranslateIAMGCPRejectsAWSPolicies(t *testing.T) {
	_, err := TranslateIAM(context.Background(), nil, IAMSpec{
		Name: "r", Provider: "gcp",
		ManagedPolicyARNs: []string{"arn:aws:iam::aws:policy/X"},
	})
	if err == nil || !strings.Contains(err.Error(), "do not map to GCP") {
		t.Errorf("expected GCP policy-mapping error, got %v", err)
	}
}

func TestAssembleHCLIAMComponent(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "app-role", Type: "iam", IAM: &AssembleIAM{
				InstanceProfile:   true,
				ManagedPolicyARNs: []string{"arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL iam: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "resource \"aws_iam_role\" \"app-role\"") ||
		!strings.Contains(all, "resource \"aws_iam_instance_profile\" \"app-role\"") {
		t.Errorf("assembled IAM HCL missing role/instance-profile:\n%s", all)
	}
}

func TestAssembleHCLAccessPolicyAWS(t *testing.T) {
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
		Name:     "demo",
		Provider: "aws",
		Region:   "Dublin",
		Components: []AssembleComponent{{
			Name: "app-policy",
			Type: "access-policy",
			IAM: &AssembleIAM{InlinePolicies: []IAMPolicy{{
				Name:     "s3",
				Document: `{"Version":"2012-10-17","Statement":[]}`,
			}}},
		}},
	})
	if err != nil {
		t.Fatalf("AssembleHCL access-policy aws: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "aws_iam_policy" "app-policy-s3"`,
		`policy = <<-PYXIAMPOLICY`,
		`"Version":"2012-10-17"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("AWS access-policy HCL missing %q\n%s", want, all)
		}
	}
	if strings.Contains(all, "# pyxcloud mitigation:") {
		t.Errorf("access-policy should not use VM mitigation:\n%s", all)
	}
}

func TestAssembleHCLAccessPolicyPortable(t *testing.T) {
	docs, err := AssembleHCL(context.Background(), MustEmbedded(), AssembleInput{
		Name:     "demo",
		Provider: ProviderDigitalOcean,
		Region:   "Frankfurt",
		Components: []AssembleComponent{{
			Name: "app-policy",
			Type: "access-policy",
			IAM: &AssembleIAM{InlinePolicies: []IAMPolicy{{
				Name:     "s3",
				Document: `{"Version":"2012-10-17","Statement":[]}`,
			}}},
		}},
	})
	if err != nil {
		t.Fatalf("AssembleHCL access-policy portable: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "terraform_data" "app-policy-s3"`,
		`provider = "digitalocean"`,
		`document = <<-PYXIAMPOLICY`,
		`"Version":"2012-10-17"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("portable access-policy HCL missing %q\n%s", want, all)
		}
	}
	if strings.Contains(all, "# pyxcloud mitigation:") || strings.Contains(all, "resource \"digitalocean_droplet\"") {
		t.Errorf("access-policy should not self-host on VM:\n%s", all)
	}
}
