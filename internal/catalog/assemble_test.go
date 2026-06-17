package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestAssembleHCLAWSVMEnv(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name:     "demo",
		Provider: "aws",
		Region:   "Dublin", // -> eu-west-1, has SKUs in the snapshot
		Expose:   []int{22},
		Components: []AssembleComponent{
			{Name: "app", Type: "virtual-machine", Count: 1,
				VM: &AssembleVM{Architecture: "x86_64", CPU: "2", RAM: "4", OS: "ubuntu"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("expected 3 docs (network, sg, vm), got %d", len(docs))
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		"resource \"aws_vpc\"",
		"resource \"aws_subnet\"",
		"resource \"aws_security_group\"",
		"resource \"aws_instance\"",
		"vpc_security_group_ids = [aws_security_group.demo-sg.id]", // VM wired to the synthesised SG
	} {
		if !strings.Contains(all, want) {
			t.Errorf("assembled HCL missing %q\n---\n%s", want, all)
		}
	}
}

func TestAssembleHCLUnsupportedTypeErrors(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	_, err = AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{{Name: "x", Type: "quantum-computer"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not yet supported") {
		t.Errorf("expected unsupported-type error, got %v", err)
	}
}

func TestAssembleHCLObjectStorageAndSecrets(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "assets", Type: "object-storage", ObjectStorage: &AssembleObjectStorage{Versioning: true}},
			{Name: "appsecret", Type: "secrets-manager", Secrets: &AssembleSecrets{Description: "app secret"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL os+secrets: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "aws_s3_bucket") {
		t.Errorf("missing s3 bucket:\n%s", all)
	}
	if !strings.Contains(all, "aws_secretsmanager_secret") {
		t.Errorf("missing secretsmanager secret:\n%s", all)
	}
	if strings.Contains(all, "aws_vpc") {
		t.Errorf("storage/secrets-only env must not synthesise a VPC:\n%s", all)
	}
}
