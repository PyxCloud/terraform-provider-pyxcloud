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
		"data \"aws_vpc\" \"default\"",
		"data \"aws_subnet\" \"demo-net_1\"",
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
				Listeners:  []AssembleLBListener{{Port: 80, Protocol: "http"}},
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

func TestAssembleHCLMitigationSelfHostsOnVM(t *testing.T) {
	cat, _ := NewEmbedded()
	// secrets-manager is NOT native on DigitalOcean -> mitigate (self-host Vault on a droplet).
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "vault", Type: "secrets-manager", Secrets: &AssembleSecrets{}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL mitigation: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "digitalocean_droplet") {
		t.Errorf("mitigation should self-host on a droplet:\n%s", all)
	}
	if !strings.Contains(all, "hashicorp/vault") {
		t.Errorf("mitigation should run the Vault container:\n%s", all)
	}
	if strings.Contains(all, "aws_secretsmanager_secret") || strings.Contains(all, "digitalocean_vpc") == false {
		// must NOT use the managed service; must have a VPC for the droplet
		t.Errorf("mitigation env shape wrong:\n%s", all)
	}
}

func TestAssembleHCLNativeStillUsedWhenSupported(t *testing.T) {
	cat, _ := NewEmbedded()
	// secrets-manager IS native on AWS -> use the managed service, no VM.
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{{Name: "s", Type: "secrets-manager", Secrets: &AssembleSecrets{}}},
	})
	if err != nil {
		t.Fatalf("AssembleHCL native: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "aws_secretsmanager_secret") || strings.Contains(all, "aws_instance") {
		t.Errorf("AWS should use the managed secret, no VM:\n%s", all)
	}
}

func TestAssembleHCLBlockStorageAndPrefixList(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "data", Type: "virtual-machine", Count: 1, VM: &AssembleVM{Architecture: "x86_64", CPU: "2", RAM: "4", OS: "ubuntu"}},
			{Name: "datavol", Type: "block-storage", BlockStorage: &AssembleBlockStorage{SizeGB: 100, TargetVM: "data"}},
			{Name: "office", Type: "prefix-list", PrefixList: &AssemblePrefixList{Entries: []PrefixEntry{{CIDR: "203.0.113.0/24", Description: "office"}}}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL ebs+prefixlist: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{"aws_ebs_volume", "aws_volume_attachment", "instance_id = aws_instance.data-1.id", "aws_ec2_managed_prefix_list"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q\n---\n%s", want, all)
		}
	}
}

func TestAssembleHCLSynthetics(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "login-canary", Type: "synthetics", Synthetics: &AssembleSynthetics{ScheduleExpr: "rate(1 minute)"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL synthetics: %v", err)
	}
	if !strings.Contains(strings.Join(docs, "\n"), "aws_synthetics_canary") {
		t.Errorf("synthetics env missing canary:\n%s", strings.Join(docs, "\n"))
	}
}

func TestAssembleHCLAttachToExistingALB(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name:     "demo",
		Provider: "aws",
		Region:   "Dublin",
		Components: []AssembleComponent{
			{
				Name: "api-asg",
				Type: "virtual-machine-scale-group",
				ScaleGroup: &AssembleScaleGroup{
					Architecture: "x86_64",
					CPU:          "2",
					RAM:          "4",
					OS:           "ubuntu",
					Min:          1,
					Max:          5,
					Desired:      2,
					Health:       "elb",
				},
			},
			{
				Name: "api-attach",
				Type: "attach-to-existing-alb",
				AttachToExistingALB: &AssembleAttachToExistingALB{
					ALBListenerARN:  "arn:aws:elasticloadbalancing:eu-west-1:123456789012:listener/app/shared-alb/123456",
					HostHeader:      "api.pyxcloud.local",
					Port:            8080,
					Protocol:        "http",
					HealthCheckPath: "/health",
					ScaleGroup:      "api-asg",
					Priority:        100,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL attach-to-existing-alb: %v", err)
	}

	all := strings.Join(docs, "\n")
	for _, want := range []string{
		"resource \"aws_lb_target_group\" \"api-attach_tg\"",
		"resource \"aws_lb_listener_rule\" \"api-attach_rule\"",
		"resource \"aws_autoscaling_attachment\" \"api-attach_attach\"",
		"listener_arn = \"arn:aws:elasticloadbalancing:eu-west-1:123456789012:listener/app/shared-alb/123456\"",
		"priority     = 100",
		"values = [\"api.pyxcloud.local\"]",
		"autoscaling_group_name = aws_autoscaling_group.api-asg_asg.name",
		"target_group_arn = aws_lb_target_group.api-attach_tg.arn",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q\n---\n%s", want, all)
		}
	}
}

// TestAssembleHCLMigrationScheduledTriggerAndKVAWS is the plan-only round-trip for
// the two AWS->DO migration components on the SOURCE (AWS) side: a scheduled
// trigger (EventBridge cron) plus a key-value store (DynamoDB) assemble together.
func TestAssembleHCLMigrationScheduledTriggerAndKVAWS(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "mig", Provider: "aws", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "nightly", Type: "scheduled-trigger", ScheduledTrigger: &AssembleScheduledTrigger{
				Schedule: "cron(0 3 * * ? *)", InvokeTarget: "arn:aws:lambda:eu-central-1:123:function:f"}},
			{Name: "jit-allowlist", Type: "key-value-store", KeyValueStore: &AssembleKeyValueStore{PartitionKey: "user_id"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL scheduled-trigger+kv (aws): %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "aws_cloudwatch_event_rule"`,
		`schedule_expression = "cron(0 3 * * ? *)"`,
		`resource "aws_dynamodb_table"`,
		`billing_mode = "PAY_PER_REQUEST"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q\n---\n%s", want, all)
		}
	}
}

// TestAssembleHCLMigrationScheduledTriggerAndKVDO is the plan-only round-trip for
// the two components on the TARGET (DigitalOcean) side: the scheduled trigger
// becomes a DOKS CronJob and the KV store becomes Managed Redis, with the synthesised
// VPC the Redis cluster attaches to.
func TestAssembleHCLMigrationScheduledTriggerAndKVDO(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "mig", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "nightly", Type: "scheduled-trigger", ScheduledTrigger: &AssembleScheduledTrigger{
				Schedule: "cron(0 3 * * ? *)", Image: "registry.example/job:latest", ClusterName: "prod-doks"}},
			{Name: "jit-allowlist", Type: "key-value-store", KeyValueStore: &AssembleKeyValueStore{MemoryGB: 1}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL scheduled-trigger+kv (do): %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "kubernetes_cron_job_v1"`,
		`schedule                      = "0 3 * * *"`,
		`image = "registry.example/job:latest"`,
		`resource "digitalocean_database_cluster"`,
		`engine     = "redis"`,
		`resource "digitalocean_vpc"`,
		`private_network_uuid = digitalocean_vpc.mig-net.id`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q\n---\n%s", want, all)
		}
	}
}

// TestAssembleHCLDONetNewComponents proves a DO topology using the net-new
// migration components (container-registry + reserved-ip) assembles into valid
// DO HCL — the plan-only round-trip for EPIC-AWS-TO-DO-MIGRATION. It also asserts
// the digitalocean provider source is pinned (required for `terraform plan`).
func TestAssembleHCLDONetNewComponents(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "do-mig", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "app-images", Type: "container-registry",
				ContainerRegistry: &AssembleContainerRegistry{Tier: "professional", GarbageCollection: true}},
			{Name: "vpn-endpoint", Type: "reserved-ip",
				ReservedIP: &AssembleReservedIP{}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL DO net-new: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`digitalocean = {`,                           // provider source pinned
		`source = "digitalocean/digitalocean"`,       //
		`resource "digitalocean_container_registry"`, // container-registry target
		`subscription_tier_slug = "professional"`,    //
		`region                 = "fra1"`,            //
		`resource "digitalocean_reserved_ip"`,        // reserved-ip target
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO net-new HCL missing %q\n%s", want, all)
		}
	}
	// region-scoped components must NOT synthesise a VPC.
	if strings.Contains(all, "digitalocean_vpc") {
		t.Errorf("registry/reserved-ip-only env must not synthesise a VPC:\n%s", all)
	}
}

// TestAssembleHCLObjectStorageParityDO is the plan-only round-trip for
// pd-MIG-OBJSTORE-PARITY: an object-store carrying lifecycle + SSE + bucket-policy
// + access-logs assembles into valid DO Spaces HCL (the S3->Spaces migration),
// with the provider source pinned for `terraform plan`.
func TestAssembleHCLObjectStorageParityDO(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "os-mig", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "assets", Type: "object-storage", ObjectStorage: &AssembleObjectStorage{
				Versioning: true,
				Lifecycle: []LifecycleRule{
					{ID: "expire-tmp", Prefix: "tmp/", Enabled: true, ExpireDays: 30},
					{ID: "abort-mpu", Enabled: true, AbortIncompleteMultipartDays: 7},
				},
				SSE:          &SSEConfig{Algorithm: SSEAlgoAES256},
				BucketPolicy: `{"Version":"2012-10-17","Statement":[]}`,
				AccessLogs:   &AccessLogConfig{TargetBucket: "audit-logs"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL object-storage parity (do): %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`source = "digitalocean/digitalocean"`,
		`resource "digitalocean_spaces_bucket" "assets"`,
		`lifecycle_rule {`,
		`abort_incomplete_multipart_upload_days = 7`,
		`resource "digitalocean_spaces_bucket_policy" "assets"`,
		`# server-side encryption (AES256)`,
		`# NOTE: server access logging`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO object-storage parity HCL missing %q\n%s", want, all)
		}
	}
}

// TestAssembleHCLTLSCertManagerDO is the plan-only round-trip for
// pd-MIG-TLS-CERTMANAGER: an ACM cert migrated to cert-manager + Let's Encrypt on
// DOKS assembles into valid DO HCL, pinning BOTH the digitalocean and kubernetes
// providers (required for `terraform plan` of the kubernetes_manifest resources).
func TestAssembleHCLTLSCertManagerDO(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "tls-mig", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "app-tls", Type: "tls-certificate", TLSCertificate: &AssembleTLSCertificate{
				Domains: []string{"app.example.com"}, Email: "ops@example.com",
				ClusterName: "prod-doks", Production: true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL tls-certificate (do): %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`source = "digitalocean/digitalocean"`, // cloud provider pinned
		`kubernetes = {`,                       // kubernetes provider pinned
		`source = "hashicorp/kubernetes"`,      //
		`resource "kubernetes_manifest" "app-tls_issuer"`,
		`kind       = "ClusterIssuer"`,
		`resource "kubernetes_manifest" "app-tls_certificate"`,
		`name = "letsencrypt-prod"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO cert-manager HCL missing %q\n%s", want, all)
		}
	}
}

// TestAssembleHCLTLSCertManagerAWS is the ACM-peer plan-only round-trip.
func TestAssembleHCLTLSCertManagerAWS(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "tls-aws", Provider: "aws", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "app-tls", Type: "tls-certificate", TLSCertificate: &AssembleTLSCertificate{
				Domains: []string{"app.example.com", "www.example.com"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL tls-certificate (aws): %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "aws_acm_certificate" "app-tls"`,
		`validation_method = "DNS"`,
		`subject_alternative_names = ["www.example.com"]`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("AWS ACM HCL missing %q\n%s", want, all)
		}
	}
	// AWS-only env: no required_providers block (hashicorp namespace auto-installs).
	if strings.Contains(all, "required_providers") {
		t.Errorf("AWS-only env should emit no required_providers block:\n%s", all)
	}
}

// TestAssembleHCLObjectStorageParityAWS is the AWS-peer plan-only round-trip:
// the same canonical parity intent renders the four AWS v4+ sub-resources.
func TestAssembleHCLObjectStorageParityAWS(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "os-aws", Provider: "aws", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "assets", Type: "object-storage", ObjectStorage: &AssembleObjectStorage{
				Versioning:   true,
				Lifecycle:    []LifecycleRule{{ID: "expire-tmp", Enabled: true, ExpireDays: 30}},
				SSE:          &SSEConfig{Algorithm: SSEAlgoKMS, KMSKeyID: "arn:aws:kms:eu-central-1:111:key/abc"},
				BucketPolicy: `{"Version":"2012-10-17","Statement":[]}`,
				AccessLogs:   &AccessLogConfig{TargetBucket: "audit-logs", TargetPrefix: "s3/"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL object-storage parity (aws): %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "aws_s3_bucket_server_side_encryption_configuration" "assets"`,
		`sse_algorithm     = "aws:kms"`,
		`kms_master_key_id = "arn:aws:kms:eu-central-1:111:key/abc"`,
		`resource "aws_s3_bucket_lifecycle_configuration" "assets"`,
		`resource "aws_s3_bucket_policy" "assets"`,
		`resource "aws_s3_bucket_logging" "assets"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("AWS object-storage parity HCL missing %q\n%s", want, all)
		}
	}
}

// TestAssembleHCLScaleGroupDOKS is the plan-only round-trip for
// pd-MIG-SCALEGROUP-DOKS: an abstract virtual-machine-scale-group placed on
// DigitalOcean assembles into a valid digitalocean_kubernetes_cluster with an
// auto-scaling node_pool (the AWS->DO migration keystone — DO has no native VM
// ASG, so the scale-group maps to a DOKS node pool). It asserts the provider
// source is pinned, the place's VPC is synthesised and wired, and the self-heal
// min/max/desired bounds carry through.
func TestAssembleHCLScaleGroupDOKS(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "sg-mig", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{
				Name: "web",
				Type: "virtual-machine-scale-group",
				ScaleGroup: &AssembleScaleGroup{
					Architecture:      "x86_64",
					CPU:               "2",
					RAM:               "4",
					OS:                "ubuntu",
					Min:               1,
					Max:               5,
					Desired:           2,
					Health:            "elb",
					KubernetesVersion: "1.30",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL scale-group DOKS: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`source = "digitalocean/digitalocean"`,            // provider source pinned
		`resource "digitalocean_vpc" "sg-mig-net"`,        // place VPC synthesised
		`resource "digitalocean_kubernetes_cluster" "web"`, // scale-group -> DOKS
		`vpc_uuid = digitalocean_vpc.sg-mig-net.id`,       // node pool joins the VPC
		`node_pool {`,
		`auto_scale = true`,
		`min_nodes  = 1`, // self-heal: ASG-of-1 floor
		`max_nodes  = 5`,
		`node_count = 2`,
		`version = "1.30"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO scale-group DOKS HCL missing %q\n%s", want, all)
		}
	}
	// DO has no native VM ASG: the AWS launch-template / autoscaling-group must
	// NOT appear.
	if strings.Contains(all, "aws_autoscaling_group") || strings.Contains(all, "aws_launch_template") {
		t.Errorf("DO scale-group must not emit AWS ASG resources:\n%s", all)
	}
}
