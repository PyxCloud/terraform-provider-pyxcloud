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
		{
			name:       "digitalocean secrets-manager uses Vault",
			provider:   ProviderDigitalOcean,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "secrets", Type: "secrets-manager", Secrets: &AssembleSecrets{}},
			vmResource: "resource \"digitalocean_droplet\"",
			image:      "hashicorp/vault",
		},
		{
			name:       "ubicloud cache uses Redis",
			provider:   ProviderUbicloud,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "cache", Type: "cache", Cache: &AssembleCache{}},
			vmResource: "resource \"ubicloud_vm\"",
			image:      "redis:7",
		},
		{
			name:       "digitalocean managed-queue uses RabbitMQ",
			provider:   ProviderDigitalOcean,
			region:     "Frankfurt",
			component:  AssembleComponent{Name: "jobs", Type: "managed-queue", Queue: &AssembleQueue{}},
			vmResource: "resource \"digitalocean_droplet\"",
			image:      "rabbitmq:3",
		},
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
			component: "secrets-manager",
			native:    []string{ProviderAWS, ProviderGCP, ProviderAzure, ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderStackIt},
			fallback:  []string{ProviderDigitalOcean, ProviderLinode, ProviderUbicloud, ProviderOVH},
		},
		{
			component: "managed-queue",
			native:    []string{ProviderAWS, ProviderGCP, ProviderAzure, ProviderOracle, ProviderAlibaba},
			fallback:  []string{ProviderDigitalOcean, ProviderLinode, ProviderUbicloud, ProviderIBM, ProviderOVH, ProviderStackIt},
		},
		{
			component: "event-streaming",
			native:    []string{ProviderAWS, ProviderGCP, ProviderAzure, ProviderOracle, ProviderIBM, ProviderAlibaba},
			fallback:  []string{ProviderDigitalOcean, ProviderLinode, ProviderUbicloud, ProviderOVH, ProviderStackIt},
		},
		{
			component: "managed-database",
			native: []string{
				ProviderAWS, ProviderGCP, ProviderDigitalOcean, ProviderAzure, ProviderLinode,
				ProviderUbicloud, ProviderOracle, ProviderIBM, ProviderAlibaba, ProviderOVH, ProviderStackIt,
			},
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
