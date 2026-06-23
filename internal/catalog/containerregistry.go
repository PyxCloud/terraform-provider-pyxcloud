package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Container registry is the abstract `container-registry` component — the
// net-new DigitalOcean migration target that replaces AWS ECR (board task
// pd-MIG-CONTAINER-REGISTRY, epic EPIC-AWS-TO-DO-MIGRATION). Like
// object-storage it has NO sizing catalog: a registry is account/region-scoped
// and billed per tier, so the only catalog lookup is the region (region_name +
// provider -> csp_region). The component therefore depends on the RegionCatalog
// only, exactly like the network, load-balancer and object-storage components.
//
// SCOPE (SPEC §5 ethos: "no exotic products, map cleanly across providers"):
//   - AWS:          aws_ecr_repository           (the ECR being migrated FROM)
//   - GCP:          google_artifact_registry_repository (DOCKER format)
//   - DigitalOcean: digitalocean_container_registry      (the migration target)
//
// DigitalOcean's registry is ACCOUNT-GLOBAL (one registry per account, pinned to
// one region), with a subscription TIER (starter | basic | professional) and an
// optional GARBAGE-COLLECTION cadence. We carry tier + garbage_collection in the
// plan where DO supports them; AWS/GCP have no tier concept (repositories are
// per-image-name, billed per-GB) so the tier is DO-only and silently irrelevant
// on the other providers (never emitted there).

// Canonical container-registry type token. `container-registry` is canonical;
// `image-registry` is an accepted alias (both name the same component).
const (
	TypeContainerRegistry = "container-registry"
	TypeImageRegistry     = "image-registry"
)

// DO registry subscription tiers (digitalocean_container_registry.subscription_tier_slug).
const (
	RegistryTierStarter      = "starter"      // 1 repo, 500 MB, free
	RegistryTierBasic        = "basic"        // 5 repos, 5 GB
	RegistryTierProfessional = "professional" // unlimited repos, 100 GB
)

// ContainerRegistrySpec is the abstract description of a container/image registry
// — the canonical `container-registry { name, tier, garbage_collection }`, placed
// in the place's region. Provider-neutral.
type ContainerRegistrySpec struct {
	Name     string // registry/component name, e.g. "app-images"
	Region   string // abstract pyx region_name, e.g. "Frankfurt"
	Provider string // provider-facing name: aws | gcp | digitalocean

	// Tier is the DO subscription tier (starter | basic | professional). Optional;
	// defaults to "basic" when empty. Ignored on providers without a tier concept
	// (AWS ECR / GCP Artifact Registry) — never emitted there.
	Tier string

	// GarbageCollection requests that DO run garbage collection to reclaim space
	// from untagged manifests. DO exposes this only on the registry, so it is a
	// DO-only toggle; defaults to false (opt-in, mirrors the conservative default
	// of the other components). Ignored on AWS/GCP.
	GarbageCollection bool

	// ImmutableTags requests immutable image tags where the provider supports it
	// (AWS ECR image_tag_mutability = IMMUTABLE; GCP Artifact Registry does not
	// expose per-repo immutability on the resource). Defaults to false. DO has no
	// per-tag immutability toggle on the registry resource, so it is ignored on DO.
	ImmutableTags bool
}

// ContainerRegistryPlan is the deterministic, catalog-resolved concrete
// translation of a ContainerRegistrySpec for one provider. STRUCTURED plan (not
// rendered .tf) — the provider owns rendering and state, consistent with the
// other components (§8).
type ContainerRegistryPlan struct {
	Provider   string `json:"provider"`    // aws | gcp | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | gcp | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)

	// RegistryName is the sanitised concrete registry/repository name. DO registry
	// names are GLOBALLY UNIQUE across all DO accounts and must be DNS-compatible
	// (lowercase letters, digits, hyphens), so on DO we derive a globally-safe name
	// exactly like object-storage buckets (sanitised prefix + deterministic hash).
	// AWS/GCP names are account-scoped, so there the logical name is used verbatim
	// (sanitised only to the provider charset).
	RegistryName string `json:"registry_name"`
	LogicalName  string `json:"logical_name"` // the user's abstract name (tf resource label)

	Tier              string `json:"tier"`               // resolved DO tier (DO only; "" elsewhere)
	GarbageCollection bool   `json:"garbage_collection"` // DO garbage-collection (DO only)
	ImmutableTags     bool   `json:"immutable_tags"`     // AWS ECR IMMUTABLE tags (AWS only)

	ResourceType string `json:"resource_type"` // top provider resource, e.g. digitalocean_container_registry
}

// ContainerRegistryCatalog is the resolution boundary for container registries.
// Only region resolution is needed (no sizing table), so RegionCatalog suffices.
type ContainerRegistryCatalog = RegionCatalog

// TranslateContainerRegistry resolves a ContainerRegistrySpec into a concrete
// ContainerRegistryPlan using the catalog. Deterministic and catalog-driven: the
// csp_region comes from the region catalog (never invented), the registry name is
// derived to be globally-unique-safe on DO, and any missing catalog data surfaces
// as a hard plan-time error (never a silent fallback), per SPEC §4.
func TranslateContainerRegistry(ctx context.Context, cat ContainerRegistryCatalog, spec ContainerRegistrySpec) (ContainerRegistryPlan, error) {
	if err := validateContainerRegistrySpec(spec); err != nil {
		return ContainerRegistryPlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return ContainerRegistryPlan{}, err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))

	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = "pyxcloud-registry"
	}

	plan := ContainerRegistryPlan{
		Provider:    provider,
		CSP:         row.CSP,
		RegionName:  row.RegionName,
		CSPRegion:   row.CSPRegion,
		LogicalName: name,
	}

	switch provider {
	case ProviderAWS:
		// ECR repositories are account+region scoped; the name is used verbatim
		// (lower-cased to the ECR charset). Tier/GC are DO concepts — not emitted.
		plan.RegistryName = sanitiseRegistryName(name)
		plan.ImmutableTags = spec.ImmutableTags
		plan.ResourceType = "aws_ecr_repository"
	case ProviderGCP:
		// Artifact Registry repositories are project+location scoped; name verbatim.
		plan.RegistryName = sanitiseRegistryName(name)
		plan.ResourceType = "google_artifact_registry_repository"
	case ProviderDigitalOcean:
		// DO registry names share a GLOBAL namespace across all accounts, so derive
		// a globally-unique-safe name (sanitised prefix + deterministic hash),
		// reusing the object-storage bucket-name contract.
		plan.RegistryName = deriveBucketName(name, row.CSP, row.CSPRegion)
		plan.Tier = resolveRegistryTier(spec.Tier)
		plan.GarbageCollection = spec.GarbageCollection
		plan.ResourceType = "digitalocean_container_registry"
	default:
		// container-registry is a wave-1 (aws/gcp/do) component. Other providers are
		// rejected at render time with a clear, actionable message (no silent
		// fallback), so the plan carries no resource type for them.
	}
	return plan, nil
}

// resolveRegistryTier defaults an empty/unknown DO tier to "basic" — a sane
// production default (5 GB, 5 repos). An explicit valid tier is honoured.
func resolveRegistryTier(tier string) string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case RegistryTierStarter:
		return RegistryTierStarter
	case RegistryTierProfessional:
		return RegistryTierProfessional
	case RegistryTierBasic, "":
		return RegistryTierBasic
	default:
		return RegistryTierBasic
	}
}

// sanitiseRegistryName lower-cases and reduces s to the registry charset
// [a-z0-9-] (ECR allows a richer charset, but the lowest common denominator
// keeps the same logical name shaping identically across providers), collapsing
// runs of invalid chars to a single hyphen and trimming leading/trailing hyphens.
func sanitiseRegistryName(s string) string {
	out := sanitiseBucketPrefix(s) // identical DNS-style rules
	if out == "" {
		out = "pyx-registry"
	}
	return out
}

func validateContainerRegistrySpec(spec ContainerRegistrySpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("container-registry: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("container-registry: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("container-registry: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if t := strings.ToLower(strings.TrimSpace(spec.Tier)); t != "" &&
		t != RegistryTierStarter && t != RegistryTierBasic && t != RegistryTierProfessional {
		return fmt.Errorf(
			"container-registry: invalid tier %q (starter | basic | professional); "+
				"tier applies only to digitalocean", spec.Tier)
	}
	return nil
}

// CanonicalContainerRegistryType maps an accepted type token (container-registry
// / image-registry) to the canonical container-registry token, reporting whether
// it is a recognised type.
func CanonicalContainerRegistryType(t string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case TypeContainerRegistry, TypeImageRegistry:
		return TypeContainerRegistry, true
	default:
		return "", false
	}
}
