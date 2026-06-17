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

func TestAssembleHCLManagedDatabase(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "db", Type: "managed-database", MDB: &AssembleMDB{
				Engine: "postgres", Version: "16", CPU: 2, RAM: 4, StorageGB: 50, Encrypted: true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL mdb: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "aws_db_instance") || !strings.Contains(all, "aws_db_subnet_group") {
		t.Errorf("mdb env missing db instance/subnet group:\n%s", all)
	}
	if !strings.Contains(all, "aws_vpc") {
		t.Errorf("mdb env must synthesise a VPC for the subnet group:\n%s", all)
	}
}

func TestAssembleHCLMessagingAndServerless(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "jobs", Type: "managed-queue", Queue: &AssembleQueue{}},
			{Name: "events", Type: "event-streaming", Stream: &AssembleStream{Shards: 1}},
			{Name: "fn", Type: "serverless-function", Serverless: &AssembleServerless{Runtime: "python", Handler: "main.handler"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL messaging+serverless: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{"aws_sqs_queue", "aws_kinesis_stream", "aws_lambda_function"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q\n---\n%s", want, all)
		}
	}
}

func TestAssembleHCLKMS(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "data-key", Type: "kms", KMS: &AssembleKMS{Description: "data encryption", RotationDays: 365}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL kms: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "aws_kms_key") || !strings.Contains(all, "aws_kms_alias") {
		t.Errorf("kms env missing key/alias:\n%s", all)
	}
	if !strings.Contains(all, "enable_key_rotation     = true") {
		t.Errorf("kms rotation not enabled:\n%s", all)
	}
}

func TestAssembleHCLLoadBalancer(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin", Expose: []int{80},
		Components: []AssembleComponent{
			{Name: "web", Type: "virtual-machine", Count: 1, VM: &AssembleVM{Architecture: "x86_64", CPU: "2", RAM: "4", OS: "ubuntu"}},
			{Name: "web-lb", Type: "load-balancer", LB: &AssembleLB{
				Listeners: []AssembleLBListener{{Port: 80, Protocol: "http"}},
				TargetKind: "vm", TargetName: "web", HealthCheckPath: "/",
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL lb: %v", err)
	}
	if !strings.Contains(strings.Join(docs, "\n"), "aws_lb") {
		t.Errorf("lb env missing aws_lb:\n%s", strings.Join(docs, "\n"))
	}
}

func TestAssembleHCLEmailSES(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "mail", Type: "email", Email: &AssembleEmail{Domain: "passo.build"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL email: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "aws_ses_domain_identity") || !strings.Contains(all, "aws_ses_domain_dkim") {
		t.Errorf("email env missing SES identity/dkim:\n%s", all)
	}
	if strings.Contains(all, "aws_vpc") {
		t.Errorf("email-only env must not synthesise a VPC:\n%s", all)
	}
}
