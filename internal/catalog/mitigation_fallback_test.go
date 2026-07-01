package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestAssembleHCLFallbackServicesSelfHostOnVM(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}

	tests := []struct {
		name       string
		provider   string
		region     string
		component  AssembleComponent
		vmResource string
		image      string
	}{
		{
			name:       "ubicloud object-storage uses MinIO",
			provider:   ProviderUbicloud,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "assets", Type: "object-storage", ObjectStorage: &AssembleObjectStorage{}},
			vmResource: "resource \"ubicloud_vm\"",
			image:      "minio/minio",
		},
		// NOTE: "digitalocean secrets-manager" is no longer a VM mitigation — it is
		// auto-aliased to the Vault-HA operator (pd-MIG-B4-SECRETS-VAULT-AUTOALIAS).
		// See TestAssembleHCLB4SecretsVaultAutoAlias in assemble_test.go.
		{
			name:       "ubicloud cache uses Redis",
			provider:   ProviderUbicloud,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "cache", Type: "cache", Cache: &AssembleCache{}},
			vmResource: "resource \"ubicloud_vm\"",
			image:      "redis:7",
		},
		// NOTE: digitalocean managed-queue no longer uses the single-VM RabbitMQ
		// mitigation — it routes to the RabbitMQ Cluster Operator on DOKS (B1:
		// pd-MIG-B1-QUEUE-STREAM-OPERATORS). See messaging_operators_test.go.
		{
			name:       "ubicloud kms uses Vault Transit",
			provider:   ProviderUbicloud,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "keys", Type: "kms", KMS: &AssembleKMS{}},
			vmResource: "resource \"ubicloud_vm\"",
			image:      "hashicorp/vault",
		},
		{
			name:       "ubicloud monitoring uses Prometheus",
			provider:   ProviderUbicloud,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "metrics", Type: "monitoring", Monitoring: &AssembleMonitoring{LogGroups: []LogGroup{{Name: "app"}}}},
			vmResource: "resource \"ubicloud_vm\"",
			image:      "prom/prometheus",
		},
		{
			name:       "ubicloud managed-kubernetes uses k3s",
			provider:   ProviderUbicloud,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "cluster", Type: "managed-kubernetes", K8s: &AssembleK8s{}},
			vmResource: "resource \"ubicloud_vm\"",
			image:      "rancher/k3s",
		},
		{
			name:       "ubicloud load-balancer uses HAProxy",
			provider:   ProviderUbicloud,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "edge", Type: "load-balancer", LB: &AssembleLB{}},
			vmResource: "resource \"ubicloud_vm\"",
			image:      "haproxy:2.9",
		},
		{
			// F1-05 (BESPOKE GAP-2): email on DO no longer VM-mitigates — it renders a
			// native SMTP-relay config instead (see ses_test.go / prod_estate_test.go).
			// Other non-AWS providers (here Ubicloud) still VM-mitigate email.
			name:       "ubicloud email uses SMTP VM",
			provider:   ProviderUbicloud,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "mail", Type: "email", Email: &AssembleEmail{Domain: "example.com"}},
			vmResource: "resource \"ubicloud_vm\"",
			image:      "bytemark/smtp",
		},
		{
			name:       "ubicloud block-storage uses NFS",
			provider:   ProviderUbicloud,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "data", Type: "block-storage", BlockStorage: &AssembleBlockStorage{SizeGB: 100, TargetVM: "app"}},
			vmResource: "resource \"ubicloud_vm\"",
			image:      "itsthenetwork/nfs-server-alpine",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
				Name:       "demo",
				Provider:   tc.provider,
				Region:     tc.region,
				Components: []AssembleComponent{tc.component},
			})
			if err != nil {
				t.Fatalf("AssembleHCL fallback: %v", err)
			}
			all := strings.Join(docs, "\n")
			for _, want := range []string{
				"# pyxcloud mitigation:",
				tc.vmResource,
				tc.image,
				"user_data = <<-PYXUSERDATA",
				"docker run -d --restart=always",
			} {
				if !strings.Contains(all, want) {
					t.Fatalf("fallback HCL missing %q\n---\n%s", want, all)
				}
			}
		})
	}
}

func TestNativeSupportMatchesRendererSurface(t *testing.T) {
	tests := []struct {
		component string
		native    []string
		fallback  []string
	}{
		{
			component: "cache",
			native:    []string{ProviderAWS, ProviderGCP, ProviderDigitalOcean, ProviderAzure, ProviderOracle, ProviderIBM, ProviderAlibaba},
			fallback:  []string{ProviderLinode, ProviderUbicloud, ProviderOVH, ProviderStackIt},
		},
		{
			component: "object-storage",
			native: []string{
				ProviderAWS, ProviderGCP, ProviderDigitalOcean, ProviderAzure, ProviderLinode,
				ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderOVH, ProviderStackIt,
			},
			fallback: []string{ProviderUbicloud},
		},
		{
			// pd-MIG-B4-SECRETS-VAULT-AUTOALIAS: DO is now "native" (auto-alias to
			// Vault-HA operator) so the VM mitigation is NOT taken.
			component: "secrets-manager",
			native:    []string{ProviderAWS, ProviderGCP, ProviderAzure, ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderStackIt, ProviderDigitalOcean},
			fallback:  []string{ProviderLinode, ProviderUbicloud, ProviderOVH},
		},
		{
			// B1 (pd-MIG-B1-QUEUE-STREAM-OPERATORS): DO is now natively supported
			// via the RabbitMQ Cluster Operator on DOKS (not single-VM mitigation).
			component: "managed-queue",
			native:    []string{ProviderAWS, ProviderGCP, ProviderAzure, ProviderOracle, ProviderAlibaba, ProviderDigitalOcean},
			fallback:  []string{ProviderLinode, ProviderUbicloud, ProviderIBM, ProviderOVH, ProviderStackIt},
		},
		{
			// B1 (pd-MIG-B1-QUEUE-STREAM-OPERATORS): DO is now natively supported
			// via the Strimzi Kafka Operator on DOKS (not single-VM Redpanda mitigation).
			component: "event-streaming",
			native:    []string{ProviderAWS, ProviderGCP, ProviderAzure, ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderDigitalOcean},
			fallback:  []string{ProviderLinode, ProviderUbicloud, ProviderOVH, ProviderStackIt},
		},
		{
			component: "managed-database",
			native: []string{
				ProviderAWS, ProviderGCP, ProviderDigitalOcean, ProviderAzure, ProviderLinode,
				ProviderUbicloud, ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderOVH, ProviderStackIt,
			},
		},
		{
			component: "managed-kubernetes",
			native: []string{
				ProviderAWS, ProviderGCP, ProviderDigitalOcean, ProviderAzure, ProviderLinode,
				ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderOVH, ProviderStackIt,
			},
			fallback: []string{ProviderUbicloud},
		},
		{
			component: "load-balancer",
			native: []string{
				ProviderAWS, ProviderGCP, ProviderDigitalOcean, ProviderAzure, ProviderLinode,
				ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderStackIt,
			},
			fallback: []string{ProviderUbicloud, ProviderOVH},
		},
		{
			// F1-05 (BESPOKE GAP-2): DO is now natively supported via the SMTP-relay
			// render (AWS SES SMTP cross-cloud by default) — not the single-VM
			// mitigation. Other non-AWS providers still VM-mitigate email.
			component: "email",
			native:    []string{ProviderAWS, ProviderDigitalOcean},
			fallback:  []string{ProviderGCP, ProviderAzure, ProviderLinode, ProviderUbicloud, ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderOVH, ProviderStackIt},
		},
		{
			component: "block-storage",
			native:    []string{ProviderAWS, ProviderGCP, ProviderDigitalOcean},
			fallback:  []string{ProviderAzure, ProviderLinode, ProviderUbicloud, ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderOVH, ProviderStackIt},
		},
	}

	for _, tc := range tests {
		t.Run(tc.component, func(t *testing.T) {
			for _, provider := range tc.native {
				if !NativelySupported(tc.component, provider) {
					t.Errorf("%s should be native on %s", tc.component, provider)
				}
			}
			for _, provider := range tc.fallback {
				if NativelySupported(tc.component, provider) {
					t.Errorf("%s should fallback on %s", tc.component, provider)
				}
			}
		})
	}
}

func TestAssembleHCLNativeSupportDoesNotFallback(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}

	tests := []struct {
		name      string
		provider  string
		region    string
		component AssembleComponent
		native    string
		vm        string
	}{
		{
			name:      "azure cache remains native",
			provider:  ProviderAzure,
			region:    "Dublin",
			component: AssembleComponent{Name: "cache", Type: "cache", Cache: &AssembleCache{}},
			native:    "resource \"azurerm_redis_cache\"",
			vm:        "azurerm_linux_virtual_machine",
		},
		{
			name:      "ibm secrets-manager remains native",
			provider:  ProviderIBM,
			region:    "Frankfurt",
			component: AssembleComponent{Name: "secret", Type: "secrets-manager", Secrets: &AssembleSecrets{}},
			native:    "resource \"ibm_sm_arbitrary_secret\"",
			vm:        "ibm_is_instance",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
				Name:       "demo",
				Provider:   tc.provider,
				Region:     tc.region,
				Components: []AssembleComponent{tc.component},
			})
			if err != nil {
				t.Fatalf("AssembleHCL native: %v", err)
			}
			all := strings.Join(docs, "\n")
			if !strings.Contains(all, tc.native) {
				t.Fatalf("native HCL missing %q\n---\n%s", tc.native, all)
			}
			if strings.Contains(all, "# pyxcloud mitigation:") || strings.Contains(all, tc.vm) {
				t.Fatalf("native path should not fallback to VM\n---\n%s", all)
			}
		})
	}
}

func TestNonMitigatablePolicyComponentsStayNativeRouted(t *testing.T) {
	for _, component := range []string{"iam", "access-policy", "prefix-list"} {
		if !NativelySupported(component, ProviderDigitalOcean) {
			t.Errorf("%s should not be classified as service-mitigatable", component)
		}
		if Mitigatable(component) {
			t.Errorf("%s should not have a VM fallback recipe", component)
		}
	}
}
