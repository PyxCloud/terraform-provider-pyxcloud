package catalog

import (
	"context"
	"strings"
	"testing"
)

// TestTranslateContainerRegistryDO asserts the DO migration target: catalog
// csp_region, a globally-unique-safe registry name, the resolved tier, garbage
// collection carried, and the digitalocean_container_registry resource type.
func TestTranslateContainerRegistryDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateContainerRegistry(context.Background(), MustEmbedded(), ContainerRegistrySpec{
		Name:              "app-images",
		Region:            "Frankfurt",
		Provider:          "digitalocean",
		Tier:              "professional",
		GarbageCollection: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", plan.CSPRegion)
	}
	if plan.ResourceType != "digitalocean_container_registry" {
		t.Errorf("resource_type = %q, want digitalocean_container_registry", plan.ResourceType)
	}
	if plan.Tier != RegistryTierProfessional {
		t.Errorf("tier = %q, want professional", plan.Tier)
	}
	if !plan.GarbageCollection {
		t.Error("garbage_collection should be carried into the plan")
	}
	if !strings.HasPrefix(plan.RegistryName, "app-images-") {
		t.Errorf("DO registry name %q should be globally-unique-derived (prefix + hash)", plan.RegistryName)
	}
	if plan.LogicalName != "app-images" {
		t.Errorf("logical_name = %q, want app-images", plan.LogicalName)
	}
}

// TestTranslateContainerRegistryTierDefault asserts an empty tier defaults to
// "basic" (a sane production default), and that AWS/GCP carry no DO tier.
func TestTranslateContainerRegistryTierDefault(t *testing.T) {
	t.Parallel()
	do, err := TranslateContainerRegistry(context.Background(), MustEmbedded(), ContainerRegistrySpec{
		Name: "r", Region: "Frankfurt", Provider: "digitalocean",
	})
	if err != nil {
		t.Fatal(err)
	}
	if do.Tier != RegistryTierBasic {
		t.Errorf("default DO tier = %q, want basic", do.Tier)
	}

	aws, err := TranslateContainerRegistry(context.Background(), MustEmbedded(), ContainerRegistrySpec{
		Name: "r", Region: "Frankfurt", Provider: "aws", Tier: "professional",
	})
	if err != nil {
		t.Fatal(err)
	}
	if aws.ResourceType != "aws_ecr_repository" {
		t.Errorf("resource_type = %q, want aws_ecr_repository", aws.ResourceType)
	}
	if aws.Tier != "" {
		t.Errorf("AWS plan must carry NO DO tier, got %q", aws.Tier)
	}
	if aws.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", aws.CSPRegion)
	}
}

func TestTranslateContainerRegistryGCP(t *testing.T) {
	t.Parallel()
	plan, err := TranslateContainerRegistry(context.Background(), MustEmbedded(), ContainerRegistrySpec{
		Name: "r", Region: "Frankfurt", Provider: "gcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResourceType != "google_artifact_registry_repository" {
		t.Errorf("resource_type = %q, want google_artifact_registry_repository", plan.ResourceType)
	}
	if plan.CSPRegion != "europe-west3" {
		t.Errorf("csp_region = %q, want europe-west3", plan.CSPRegion)
	}
}

func TestTranslateContainerRegistryInvalidTier(t *testing.T) {
	t.Parallel()
	_, err := TranslateContainerRegistry(context.Background(), MustEmbedded(), ContainerRegistrySpec{
		Name: "r", Region: "Frankfurt", Provider: "digitalocean", Tier: "platinum",
	})
	if err == nil {
		t.Fatal("expected a hard plan-time error for an invalid tier")
	}
}

// TestRenderContainerRegistryDO asserts the DO HCL carries the registry resource,
// the resolved tier and region, and the garbage-collection note when requested.
func TestRenderContainerRegistryDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateContainerRegistry(context.Background(), MustEmbedded(), ContainerRegistrySpec{
		Name: "app-images", Region: "Frankfurt", Provider: "digitalocean",
		Tier: "basic", GarbageCollection: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderContainerRegistryHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "digitalocean_container_registry" "app-images"`,
		`subscription_tier_slug = "basic"`,
		`region                 = "fra1"`,
		"garbage_collection = true",
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO registry HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestRenderContainerRegistryAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateContainerRegistry(context.Background(), MustEmbedded(), ContainerRegistrySpec{
		Name: "app-images", Region: "Frankfurt", Provider: "aws", ImmutableTags: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderContainerRegistryHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_ecr_repository" "app-images"`,
		`image_tag_mutability = "IMMUTABLE"`,
		"scan_on_push = true",
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("AWS ECR HCL missing %q\n%s", want, hcl)
		}
	}
}

func TestRenderContainerRegistryUnsupportedProvider(t *testing.T) {
	t.Parallel()
	// A plan with a non-wave-1 provider must be a hard render-time error.
	_, err := RenderContainerRegistryHCL(ContainerRegistryPlan{Provider: ProviderAzure})
	if err == nil {
		t.Fatal("expected a hard render-time error for an unsupported provider")
	}
}

func TestCanonicalContainerRegistryType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"container-registry", "image-registry", "Container-Registry"} {
		if got, ok := CanonicalContainerRegistryType(in); !ok || got != TypeContainerRegistry {
			t.Errorf("CanonicalContainerRegistryType(%q) = %q,%v", in, got, ok)
		}
	}
	if _, ok := CanonicalContainerRegistryType("object-storage"); ok {
		t.Error("object-storage should not be a container-registry type")
	}
}
