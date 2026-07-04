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

// TestAssembleHCLTwoServerlessDODistinctGitVars guards against a silent variable
// collision: two DO Functions in one environment must each reference their own
// git-source variables (var.<name>_repo_url / var.<name>_branch), never a shared
// generic var.function_repo_url — terraform validate would not catch the overlap.
func TestAssembleHCLTwoServerlessDODistinctGitVars(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "board-api", Type: "serverless-function", Serverless: &AssembleServerless{Runtime: "go"}},
			{Name: "webhook-fn", Type: "serverless-function", Serverless: &AssembleServerless{Runtime: "nodejs"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL two DO serverless: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		"var.board-api_repo_url", "var.board-api_branch",
		"var.webhook-fn_repo_url", "var.webhook-fn_branch",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing per-component git var %q\n---\n%s", want, all)
		}
	}
	for _, collide := range []string{"var.function_repo_url", "var.function_branch"} {
		if strings.Contains(all, collide) {
			t.Errorf("generic git var %q would silently collide across functions\n---\n%s", collide, all)
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
	// managed-queue is NOT native on DigitalOcean -> mitigate (self-host RabbitMQ on a droplet).
	// NOTE: secrets-manager and kms on DO no longer use the VM mitigation — they are
	// auto-aliased to the Vault-HA operator-pattern (pd-MIG-B4-SECRETS-VAULT-AUTOALIAS).
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "jobs", Type: "managed-queue", Queue: &AssembleQueue{}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL mitigation: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "digitalocean_droplet") {
		t.Errorf("mitigation should self-host on a droplet:\n%s", all)
	}
	if !strings.Contains(all, "rabbitmq") {
		t.Errorf("mitigation should run the RabbitMQ container:\n%s", all)
	}
	if strings.Contains(all, "aws_sqs_queue") || !strings.Contains(all, "digitalocean_vpc") {
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

// TestAssembleHCLVPNAccessSignal proves the pyx_vpn_access signal assembles into
// the AWS JIT door (wg-jit SG + DynamoDB allowlist + Keycloak-role IAM policy)
// without synthesising a VPC — the catalog-driven replacement for internal-vpn's
// manual add-peer.sh / jit-backing terraform.
func TestAssembleHCLVPNAccessSignal(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "corp-vpn", Provider: "aws", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "vpn", Type: "vpn-access",
				VPNAccess: &AssembleVPNAccess{KeycloakRole: "beta-keycloak-ec2-role"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL vpn-access: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "aws_security_group" "vpn_jit"`,
		`resource "aws_dynamodb_table" "vpn_jit_allowlist"`,
		`resource "aws_iam_role_policy_attachment" "vpn_jit_policy"`,
		`role       = "beta-keycloak-ec2-role"`,
		`output "vpn_jit_sg_id"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("vpn-access env HCL missing %q\n%s", want, all)
		}
	}
	// The signal is region-scoped (uses the default VPC) — it must NOT synthesise a
	// VPC of its own.
	if strings.Contains(all, `resource "aws_vpc"`) {
		t.Errorf("vpn-access-only env must not synthesise a VPC:\n%s", all)
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
		`source = "hashicorp/helm"`,            // needsHelm pin (operator CORE)
		// CORE: the cert-manager operator installed via its upstream Helm chart
		`resource "helm_release" "app-tls_certmanager_operator"`,
		`chart      = "cert-manager"`,
		`{ name = "installCRDs", value = "true" }`,
		// EXTRA: our ClusterIssuer + Certificate custom resources
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
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL scale-group droplet_autoscale: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`source = "digitalocean/digitalocean"`,              // provider source pinned
		`resource "digitalocean_vpc" "sg-mig-net"`,          // place VPC synthesised
		`resource "digitalocean_droplet_autoscale" "web"`,   // scale-group -> droplet pool
		`vpc_uuid = digitalocean_vpc.sg-mig-net.id`,         // pool joins the VPC
		`config {`,
		`min_instances = 1`, // self-heal: ASG-of-1 floor
		`max_instances = 5`,
		`target_cpu_utilization = 0.6`, // elastic (min<max)
		`droplet_template {`,
		`ssh_keys = var.do_ssh_keys`,
		`tags = ["pyx-web"]`,
		`with_droplet_agent = true`,
		`variable "do_ssh_keys"`, // out-of-band ssh-keys var declared once
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO scale-group droplet_autoscale HCL missing %q\n%s", want, all)
		}
	}
	// DO scale-groups are droplet pools, not DOKS clusters.
	if strings.Contains(all, "digitalocean_kubernetes_cluster") || strings.Contains(all, "node_pool") {
		t.Errorf("DO scale-group must not emit DOKS resources (droplet lift-and-shift):\n%s", all)
	}
	// DO has no AWS ASG: the AWS launch-template / autoscaling-group must NOT appear.
	if strings.Contains(all, "aws_autoscaling_group") || strings.Contains(all, "aws_launch_template") {
		t.Errorf("DO scale-group must not emit AWS ASG resources:\n%s", all)
	}
}

// TestAssembleHCLTracingAWS asserts the tracing component assembles X-Ray on AWS.
func TestAssembleHCLTracingAWS(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "obs", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "traces", Type: "tracing", Tracing: &AssembleTracing{SamplingRate: 0.3}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL tracing aws: %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "aws_xray_group") || !strings.Contains(all, "fixed_rate     = 0.3") {
		t.Errorf("aws tracing env missing X-Ray group / sampling:\n%s", all)
	}
	// AWS-only env needs no required_providers block.
	if strings.Contains(all, "hashicorp/kubernetes") {
		t.Errorf("aws tracing must not pin kubernetes:\n%s", all)
	}
}

// TestAssembleHCLTracingDOPinsKubernetes asserts the DO tracing component
// assembles Tempo+OTel and pins hashicorp/kubernetes (kubernetes_manifest).
func TestAssembleHCLTracingDOPinsKubernetes(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "obs", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "traces", Type: "tracing", Tracing: &AssembleTracing{ClusterName: "prod-doks"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL tracing do: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`data "digitalocean_kubernetes_cluster" "traces_cluster"`,
		`resource "helm_release" "traces_otel_operator"`, // CORE upstream operator
		`kind       = "TempoStack"`,                      // EXTRA custom resource
		`kind       = "OpenTelemetryCollector"`,          // EXTRA custom resource
		`source = "hashicorp/kubernetes"`,                // needsKubernetes pin
		`source = "hashicorp/helm"`,                      // needsHelm pin (operator CORE)
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO tracing env missing %q:\n%s", want, all)
		}
	}
}

// TestAssembleHCLLoadBalancerL7DONoKubernetes asserts a DO load-balancer with L7
// routing rules NO LONGER emits a DOKS Ingress and does NOT pin
// hashicorp/kubernetes: with the scale-group rendered as a droplet_autoscale pool
// (plain droplets + LB), the LB forwards by droplet tag and the render is pure DO.
func TestAssembleHCLLoadBalancerL7DONoKubernetes(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "web-lb", Type: "load-balancer", LB: &AssembleLB{
				TargetKind: "vm", TargetName: "web",
				Listeners: []AssembleLBListener{{Port: 443, Protocol: "https", Rules: []AssembleLBRoutingRule{
					{Priority: 100, HostHeaders: []string{"admin.example.com"}, AdminVPNCIDRs: []string{"10.8.0.0/24"}},
				}}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL lb l7 do: %v", err)
	}
	all := strings.Join(docs, "\n")
	// The LB is a pure DO load-balancer forwarding by droplet tag.
	for _, want := range []string{
		`resource "digitalocean_loadbalancer" "web-lb"`,
		`droplet_tag = "pyx-web"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO L7 lb env missing %q:\n%s", want, all)
		}
	}
	// No DOKS Ingress / kubernetes provider pin any more.
	for _, bad := range []string{`kind       = "Ingress"`, "whitelist-source-range", `source = "hashicorp/kubernetes"`} {
		if strings.Contains(all, bad) {
			t.Errorf("DO L7 lb env must not emit %q (droplet_autoscale pivot):\n%s", bad, all)
		}
	}
}

// TestAssembleHCLWorkloadIdentityAWS asserts the workload-identity component
// assembles an AWS IAM role + instance profile (the peer being migrated from).
func TestAssembleHCLWorkloadIdentityAWS(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "app", Provider: "aws", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "wid", Type: "workload-identity", WorkloadIdentity: &AssembleWorkloadIdentity{
				InlinePolicies: []IAMPolicy{{Name: "read", Document: `{"Version":"2012-10-17","Statement":[]}`}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL workload-identity aws: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "aws_iam_role" "wid"`,
		`resource "aws_iam_instance_profile" "wid"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("aws workload-identity env missing %q:\n%s", want, all)
		}
	}
	// AWS-only env: no kubernetes/helm provider pin.
	if strings.Contains(all, `source = "hashicorp/kubernetes"`) {
		t.Errorf("aws workload-identity must not pin kubernetes:\n%s", all)
	}
}

// TestAssembleHCLWorkloadIdentityDOPinsKubernetes asserts the DO workload-identity
// component assembles the Vault AppRole CRs and pins hashicorp/kubernetes (the CR
// EXTRA); the CORE helm operator is owned by the vault-ha component.
func TestAssembleHCLWorkloadIdentityDOPinsKubernetes(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "app", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "wid", Type: "workload-identity", WorkloadIdentity: &AssembleWorkloadIdentity{
				ClusterName: "prod-doks",
				InlinePolicies: []IAMPolicy{{Name: "read",
					Document: "path \"secret/data/app/*\" { capabilities = [\"read\"] }"}},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL workload-identity do: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`data "digitalocean_kubernetes_cluster" "wid_cluster"`,
		`kind       = "VaultPolicy"`,
		`kind       = "VaultAuth"`,
		`output "wid_user_data"`,
		`source = "hashicorp/kubernetes"`, // needsKubernetes pin (CR EXTRA)
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO workload-identity env missing %q:\n%s", want, all)
		}
	}
}

// TestAssembleHCLVaultHADOPinsHelm asserts the vault-ha component assembles the
// official Vault Helm chart (HA Raft) CORE + config CRs EXTRA and pins both the
// helm and kubernetes providers.
func TestAssembleHCLVaultHADOPinsHelm(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "sec", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "vault", Type: "vault-ha", VaultHA: &AssembleVaultHA{
				ClusterName: "prod-doks", TransitUnseal: true,
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL vault-ha do: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`data "digitalocean_kubernetes_cluster" "vault_cluster"`,
		`resource "helm_release" "vault_operator"`, // CORE official Vault chart
		`chart      = "vault"`,
		`{ name = "server.ha.raft.enabled", value = "true" }`,
		`kind       = "VaultConnection"`,  // EXTRA CR
		`kind       = "VaultAuthGlobal"`,  // EXTRA CR
		`source = "hashicorp/kubernetes"`, // needsKubernetes pin (CR EXTRA)
		`source = "hashicorp/helm"`,       // needsHelm pin (operator CORE)
	} {
		if !strings.Contains(all, want) {
			t.Errorf("DO vault-ha env missing %q:\n%s", want, all)
		}
	}
}

// TestAssembleHCLVaultHAAndWorkloadIdentityDO asserts the two components compose:
// the vault-ha CORE operator + the workload-identity that binds to it.
func TestAssembleHCLVaultHAAndWorkloadIdentityDO(t *testing.T) {
	cat, _ := NewEmbedded()
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "platform", Provider: "digitalocean", Region: "Frankfurt",
		Components: []AssembleComponent{
			{Name: "vault", Type: "vault-ha", VaultHA: &AssembleVaultHA{ClusterName: "prod-doks", TransitUnseal: true}},
			{Name: "wid", Type: "workload-identity", WorkloadIdentity: &AssembleWorkloadIdentity{
				ClusterName: "prod-doks", DeliveryMode: "kubernetes",
			}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL vault-ha + workload-identity do: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		`resource "helm_release" "vault_operator"`, // vault-ha CORE
		`kind       = "ServiceAccount"`,            // workload-identity k8s SA
		`kind       = "VaultAuth"`,                 // workload-identity auth role
		`source = "hashicorp/helm"`,
	} {
		if !strings.Contains(all, want) {
			t.Errorf("composed DO env missing %q:\n%s", want, all)
		}
	}
}

// TestAssembleHCLPipelineControlPlane proves the pyx-lambda control-plane is
// reachable via the environment assembly path (dogfood: pd-DEP-PYXLAMBDA-CONTROLPLANE).
func TestAssembleHCLPipelineControlPlane(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name:     "demo",
		Provider: "aws",
		Region:   "Frankfurt",
		Components: []AssembleComponent{
			{Name: "pyx-ci", Type: "pipeline-control-plane",
				PipelineControlPlane: &AssemblePipelineControlPlane{
					PipelineName: "ci", GitHubOIDC: true,
					GitHubOwnerRepo: "PyxCloud/terraform-provider-pyxcloud",
				}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL: %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{
		"resource \"aws_sfn_state_machine\" \"pyx_ci\"",
		"resource \"aws_lambda_function\" \"pyx_ci_runner\"",
		"resource \"aws_ecs_cluster\" \"pyx_ci\"",
		"resource \"aws_codebuild_project\" \"pyx_ci\"",
		"resource \"aws_iam_openid_connect_provider\" \"pyx_ci_github\"",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("assembled control-plane HCL missing %q\n---\n%s", want, all)
		}
	}
}

// ── Architecture-mismatch detector tests (pd-ONTO-CAP-JR-COPYARCH) ───────────

func TestDetectArchitectureMismatches_CargoCultOperator(t *testing.T) {
	// Tracing component present but NO managed-kubernetes/container-service.
	in := AssembleInput{
		Name: "myenv", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "app", Type: "virtual-machine", VM: &AssembleVM{CPU: "2", RAM: "4", OS: "ubuntu"}},
			{Name: "traces", Type: "tracing"},
		},
	}
	findings := DetectArchitectureMismatches(in)
	found := false
	for _, f := range findings {
		if f.RuleID == "CARGO-CULT-OPERATOR" && f.ComponentName == "traces" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected CARGO-CULT-OPERATOR finding for tracing without k8s cluster, got %+v", findings)
	}
}

func TestDetectArchitectureMismatches_CargoCultOperator_ClusterPresent(t *testing.T) {
	// Tracing + managed-kubernetes -> NOT a cargo-cult.
	in := AssembleInput{
		Name: "myenv", Provider: "digitalocean", Region: "EU West",
		Components: []AssembleComponent{
			{Name: "cluster", Type: "managed-kubernetes", K8s: &AssembleK8s{NodeCPU: 4, NodeRAM: 8}},
			{Name: "traces", Type: "tracing"},
		},
	}
	findings := DetectArchitectureMismatches(in)
	for _, f := range findings {
		if f.RuleID == "CARGO-CULT-OPERATOR" {
			t.Errorf("unexpected CARGO-CULT-OPERATOR finding when cluster is present: %+v", f)
		}
	}
}

func TestDetectArchitectureMismatches_PrematureSplit(t *testing.T) {
	// Four tiny VMs -> premature microservices split.
	comps := []AssembleComponent{}
	for i := 0; i < PrematureSplitVMThreshold; i++ {
		comps = append(comps, AssembleComponent{
			Name: "svc", Type: "virtual-machine",
			VM: &AssembleVM{CPU: "1", RAM: "2", OS: "ubuntu"},
		})
	}
	findings := DetectArchitectureMismatches(AssembleInput{
		Name: "split", Provider: "aws", Region: "Dublin",
		Components: comps,
	})
	found := false
	for _, f := range findings {
		if f.RuleID == "PREMATURE-MICROSERVICES-SPLIT" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected PREMATURE-MICROSERVICES-SPLIT finding for %d tiny VMs, got %+v", PrematureSplitVMThreshold, findings)
	}
}

func TestDetectArchitectureMismatches_PrematureSplit_BelowThreshold(t *testing.T) {
	// Three tiny VMs -> below threshold, no finding.
	comps := []AssembleComponent{}
	for i := 0; i < PrematureSplitVMThreshold-1; i++ {
		comps = append(comps, AssembleComponent{
			Name: "svc", Type: "virtual-machine",
			VM: &AssembleVM{CPU: "2", RAM: "4", OS: "ubuntu"},
		})
	}
	findings := DetectArchitectureMismatches(AssembleInput{
		Name: "ok", Provider: "aws", Region: "Dublin",
		Components: comps,
	})
	for _, f := range findings {
		if f.RuleID == "PREMATURE-MICROSERVICES-SPLIT" {
			t.Errorf("unexpected PREMATURE-MICROSERVICES-SPLIT finding below threshold: %+v", f)
		}
	}
}

func TestDetectArchitectureMismatches_KubernetesForSingleVM(t *testing.T) {
	// One VM + managed-kubernetes, no scale-group -> KUBERNETES-FOR-SINGLE-VM.
	in := AssembleInput{
		Name: "overkill", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "cluster", Type: "managed-kubernetes", K8s: &AssembleK8s{NodeCPU: 4, NodeRAM: 8}},
			{Name: "app", Type: "virtual-machine", VM: &AssembleVM{CPU: "2", RAM: "4", OS: "ubuntu"}},
		},
	}
	findings := DetectArchitectureMismatches(in)
	found := false
	for _, f := range findings {
		if f.RuleID == "KUBERNETES-FOR-SINGLE-VM" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected KUBERNETES-FOR-SINGLE-VM finding, got %+v", findings)
	}
}

func TestDetectArchitectureMismatches_KubernetesWithScaleGroup_NoFinding(t *testing.T) {
	// K8s + scale-group is a legitimate pattern; no finding expected.
	in := AssembleInput{
		Name: "ok", Provider: "digitalocean", Region: "EU West",
		Components: []AssembleComponent{
			{Name: "cluster", Type: "managed-kubernetes", K8s: &AssembleK8s{NodeCPU: 4, NodeRAM: 8}},
			{Name: "workers", Type: "virtual-machine-scale-group",
				ScaleGroup: &AssembleScaleGroup{CPU: "4", RAM: "8", OS: "ubuntu", Min: 2, Max: 10}},
		},
	}
	findings := DetectArchitectureMismatches(in)
	for _, f := range findings {
		if f.RuleID == "KUBERNETES-FOR-SINGLE-VM" {
			t.Errorf("unexpected KUBERNETES-FOR-SINGLE-VM finding when scale-group present: %+v", f)
		}
	}
}

func TestDetectArchitectureMismatches_CleanTopology_NoFindings(t *testing.T) {
	// A normal AWS topology should produce no architecture findings.
	in := AssembleInput{
		Name: "clean", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "app", Type: "virtual-machine", VM: &AssembleVM{CPU: "4", RAM: "8", OS: "ubuntu"}},
			{Name: "db", Type: "managed-database", MDB: &AssembleMDB{Engine: "postgres", CPU: 2, RAM: 4}},
		},
	}
	findings := DetectArchitectureMismatches(in)
	if len(findings) != 0 {
		t.Errorf("expected no architecture findings for clean topology, got %+v", findings)
	}
}

func TestIsCargoCultOperator(t *testing.T) {
	cases := []struct {
		cType    string
		envTypes []string
		want     bool
	}{
		{"tracing", []string{"virtual-machine"}, true},
		{"tracing", []string{"virtual-machine", "managed-kubernetes"}, false},
		{"vault-ha", []string{"object-storage"}, true},
		{"vault-ha", []string{"container-service"}, false},
		{"virtual-machine", []string{"object-storage"}, false}, // not operator type
		{"monitoring", []string{}, true},
		{"monitoring", []string{"managed-kubernetes"}, false},
	}
	for _, tc := range cases {
		got := IsCargoCultOperator(tc.cType, tc.envTypes)
		if got != tc.want {
			t.Errorf("IsCargoCultOperator(%q, %v) = %v, want %v", tc.cType, tc.envTypes, got, tc.want)
		}
	}
}

func TestHasOperatorAlternative(t *testing.T) {
	yes := []string{"monitoring", "tracing", "vault-ha", "tls-certificate", "workload-identity",
		"distributed-tracing", "cert-manager", "vault", "vault-cluster", "synthetics", "uptime-check",
		"tempo", "trace-collector", "otel-tracing", "certificate", "managed-certificate",
		"instance-identity", "workload-id"}
	no := []string{"virtual-machine", "managed-database", "cache", "object-storage",
		"managed-kubernetes", "load-balancer", "secrets-manager", "kms", "dns"}
	for _, v := range yes {
		if !HasOperatorAlternative(v) {
			t.Errorf("HasOperatorAlternative(%q) = false, want true", v)
		}
	}
	for _, v := range no {
		if HasOperatorAlternative(v) {
			t.Errorf("HasOperatorAlternative(%q) = true, want false", v)
		}
	}
}

// TestAssembleHCLB4SecretsVaultAutoAlias is the plan-only round-trip for
// pd-MIG-B4-SECRETS-VAULT-AUTOALIAS: raw secrets-manager and kms/encryption-key
// components on DigitalOcean MUST route to the Vault-HA operator-pattern (HA Raft
// on DOKS) instead of the single-VM mitigation (a single droplet running Vault).
//
// Asserts:
//  - secrets-manager on DO with a managed-kubernetes component → Vault-HA Helm chart
//    + CRs (helm_release + kubernetes_manifest); NOT a digitalocean_droplet.
//  - kms on DO same topology → same Vault-HA output.
//  - encryption-key (alias of kms) same result.
//  - Both pin the helm + kubernetes providers.
//  - Without any managed-kubernetes and no explicit cluster_name, a clear error is returned.
func TestAssembleHCLB4SecretsVaultAutoAlias(t *testing.T) {
	t.Parallel()
	cat, _ := NewEmbedded()

	// ── secrets-manager on DO → Vault-HA operator (NOT single-VM mitigation) ──────
	t.Run("secrets-manager auto-aliases to vault-ha on DO", func(t *testing.T) {
		t.Parallel()
		docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
			Name: "b4-test", Provider: "digitalocean", Region: "Frankfurt",
			Components: []AssembleComponent{
				{Name: "app-doks", Type: "managed-kubernetes",
					K8s: &AssembleK8s{NodeCPU: 2, NodeRAM: 4, MinNodes: 1, MaxNodes: 3}},
				{Name: "app-secrets", Type: "secrets-manager",
					Secrets: &AssembleSecrets{Description: "app secrets"}},
			},
		})
		if err != nil {
			t.Fatalf("secrets-manager auto-alias: %v", err)
		}
		all := strings.Join(docs, "\n")
		// must use the Vault-HA Helm chart (CORE operator)
		if !strings.Contains(all, `resource "helm_release"`) {
			t.Errorf("expected helm_release for Vault-HA CORE, got:\n%s", all)
		}
		// must emit VaultConnection / VaultAuthGlobal CRs (EXTRA)
		if !strings.Contains(all, `resource "kubernetes_manifest"`) {
			t.Errorf("expected kubernetes_manifest for Vault-HA EXTRA CRs, got:\n%s", all)
		}
		// must NOT fall to the single-VM mitigation (a plain droplet)
		if strings.Contains(all, "digitalocean_droplet") {
			t.Errorf("secrets-manager on DO must NOT fall to single-VM mitigation:\n%s", all)
		}
		// must NOT use the AWS managed path
		if strings.Contains(all, "aws_secretsmanager_secret") {
			t.Errorf("secrets-manager on DO must not emit aws_secretsmanager_secret:\n%s", all)
		}
		// providers helm + kubernetes must be pinned
		if !strings.Contains(all, `"hashicorp/helm"`) {
			t.Errorf("helm provider must be pinned:\n%s", all)
		}
		if !strings.Contains(all, `"hashicorp/kubernetes"`) {
			t.Errorf("kubernetes provider must be pinned:\n%s", all)
		}
	})

	// ── kms on DO → Vault-HA operator (Transit replaces KMS) ─────────────────────
	t.Run("kms auto-aliases to vault-ha on DO", func(t *testing.T) {
		t.Parallel()
		docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
			Name: "b4-kms", Provider: "digitalocean", Region: "Frankfurt",
			Components: []AssembleComponent{
				{Name: "prod-doks", Type: "managed-kubernetes",
					K8s: &AssembleK8s{NodeCPU: 2, NodeRAM: 4, MinNodes: 1, MaxNodes: 3}},
				{Name: "data-key", Type: "kms",
					KMS: &AssembleKMS{Description: "data encryption key"}},
			},
		})
		if err != nil {
			t.Fatalf("kms auto-alias: %v", err)
		}
		all := strings.Join(docs, "\n")
		if !strings.Contains(all, `resource "helm_release"`) {
			t.Errorf("expected helm_release for Vault Transit CORE, got:\n%s", all)
		}
		if strings.Contains(all, "digitalocean_droplet") {
			t.Errorf("kms on DO must NOT fall to single-VM mitigation:\n%s", all)
		}
		// Vault chart name must be vault
		if !strings.Contains(all, `chart      = "vault"`) {
			t.Errorf("expected vault helm chart:\n%s", all)
		}
	})

	// ── encryption-key (alias of kms) → same result ───────────────────────────────
	t.Run("encryption-key auto-aliases to vault-ha on DO", func(t *testing.T) {
		t.Parallel()
		docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
			Name: "b4-enckey", Provider: "digitalocean", Region: "Frankfurt",
			Components: []AssembleComponent{
				{Name: "prod-doks", Type: "managed-kubernetes",
					K8s: &AssembleK8s{NodeCPU: 2, NodeRAM: 4, MinNodes: 1, MaxNodes: 3}},
				{Name: "enc-key", Type: "encryption-key"},
			},
		})
		if err != nil {
			t.Fatalf("encryption-key auto-alias: %v", err)
		}
		all := strings.Join(docs, "\n")
		if !strings.Contains(all, `resource "helm_release"`) {
			t.Errorf("expected helm_release for Vault-HA (encryption-key alias):\n%s", all)
		}
		if strings.Contains(all, "digitalocean_droplet") {
			t.Errorf("encryption-key on DO must NOT fall to single-VM mitigation:\n%s", all)
		}
	})

	// ── no DOKS cluster → clear error (not a silent degraded fallback) ────────────
	t.Run("secrets-manager on DO without DOKS cluster errors clearly", func(t *testing.T) {
		t.Parallel()
		_, err := AssembleHCL(context.Background(), cat, AssembleInput{
			Name: "b4-no-cluster", Provider: "digitalocean", Region: "Frankfurt",
			Components: []AssembleComponent{
				{Name: "app-secrets", Type: "secrets-manager",
					Secrets: &AssembleSecrets{}},
			},
		})
		if err == nil {
			t.Fatal("expected error when no managed-kubernetes + no cluster_name, got nil")
		}
		if !strings.Contains(err.Error(), "managed-kubernetes") {
			t.Errorf("error should mention managed-kubernetes: %v", err)
		}
	})

	// ── explicit cluster_name via VaultHA config overrides inference ──────────────
	t.Run("explicit vault_ha cluster_name used when no managed-kubernetes component", func(t *testing.T) {
		t.Parallel()
		docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
			Name: "b4-explicit", Provider: "digitalocean", Region: "Frankfurt",
			Components: []AssembleComponent{
				{Name: "app-secrets", Type: "secrets-manager",
					Secrets: &AssembleSecrets{},
					VaultHA: &AssembleVaultHA{ClusterName: "my-existing-doks"},
				},
			},
		})
		if err != nil {
			t.Fatalf("explicit cluster_name: %v", err)
		}
		all := strings.Join(docs, "\n")
		if !strings.Contains(all, `resource "helm_release"`) {
			t.Errorf("expected helm_release with explicit cluster_name:\n%s", all)
		}
	})
}
