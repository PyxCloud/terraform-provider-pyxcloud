package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Object/blob storage is the abstract `object-storage` (canonical alias
// `blob-storage`) component (SPEC §5.7). Unlike the compute/database components
// it has NO sizing catalog — an object store is region/location-scoped and
// billed per-usage, so the only catalog lookup is the region (region_name +
// provider -> csp_region/location). The component therefore depends on the
// RegionCatalog only, exactly like the network and load-balancer components.
//
// SECURITY INVARIANT (SPEC §5.7): a bucket is PRIVATE by default. `public=false`
// MUST enforce the provider's public-access block (AWS) / uniform private ACL
// (GCP/DO) — PyxCloud never emits a world-readable bucket by default. Making a
// bucket public is an explicit, opt-in choice the user must set.

// Canonical object-storage type tokens. `object-storage` is canonical;
// `blob-storage` is an accepted alias (both name the same component, mirroring
// the TopologyInspector vocabulary in SPEC §3.1).
const (
	TypeObjectStorage = "object-storage"
	TypeBlobStorage   = "blob-storage"
)

// ObjectStorageSpec is the abstract description of an object/blob store — the
// canonical `object-storage { name, versioning, public=false }`, placed in the
// place's region. Provider-neutral.
type ObjectStorageSpec struct {
	Name     string // bucket/component name, e.g. "app-assets"
	Region   string // abstract pyx region_name, e.g. "Frankfurt"
	Provider string // provider-facing name: aws | gcp | digitalocean

	Versioning bool // keep object versions (S3/GCS versioning; DO Spaces versioning)
	Public     bool // PUBLIC read access. Defaults to false (private). Opt-in only.

	// ForceDestroy allows Terraform to delete a NON-empty bucket on destroy. It
	// defaults to false (production-intent: refuse to delete a bucket that still
	// holds objects). The TEST round-trip override sets it true ONLY so a
	// just-created bucket tears down cleanly. Pointer so an unset value takes the
	// production-safe default.
	ForceDestroy *bool
}

// ObjectStoragePlan is the deterministic, catalog-resolved concrete translation
// of an ObjectStorageSpec for one provider. STRUCTURED plan (not rendered .tf) —
// the provider owns rendering and state, consistent with the other components (§8).
type ObjectStoragePlan struct {
	Provider   string `json:"provider"`    // aws | gcp | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | gcp | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region/location (catalog-resolved)

	// BucketName is the GLOBALLY-UNIQUE-SAFE concrete bucket name (see
	// deriveBucketName): the sanitised component name, suffixed with a short
	// deterministic hash of (csp, csp_region, name) so two places/providers never
	// collide on the global S3/GCS/Spaces namespace.
	BucketName  string `json:"bucket_name"`
	LogicalName string `json:"logical_name"` // the user's abstract name (for the tf resource label)

	Versioning bool `json:"versioning"` // object versioning enabled
	Public     bool `json:"public"`     // public read (opt-in; false = private + access-block enforced)

	ForceDestroy bool `json:"force_destroy"` // resolved (default false)

	ResourceType string `json:"resource_type"` // top provider resource, e.g. aws_s3_bucket
}

// maxBucketNameLen is the tightest cross-provider bucket-name length limit: S3
// and GCS both cap at 63 chars; DO Spaces at 63. We derive within this floor so
// the same logical name shapes identically everywhere.
const maxBucketNameLen = 63

// bucketHashLen is the length of the deterministic uniqueness suffix.
const bucketHashLen = 10

// ObjectStorageCatalog is the resolution boundary for object/blob storage. Only
// region resolution is needed (no sizing table), so RegionCatalog suffices — the
// embedded snapshot and a future live BE both satisfy it.
type ObjectStorageCatalog = RegionCatalog

// TranslateObjectStorage resolves an ObjectStorageSpec into a concrete
// ObjectStoragePlan using the catalog. Deterministic and catalog-driven: the
// csp_region/location comes from the region catalog (never invented), the bucket
// name is derived to be globally-unique-safe, and the private-by-default security
// invariant is carried in the plan (Public defaults false). Any missing catalog
// data surfaces as a hard plan-time error (never a silent fallback), per SPEC §4.
func TranslateObjectStorage(ctx context.Context, cat ObjectStorageCatalog, spec ObjectStorageSpec) (ObjectStoragePlan, error) {
	if err := validateObjectStorageSpec(spec); err != nil {
		return ObjectStoragePlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return ObjectStoragePlan{}, err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))

	// StackIt object storage exposes no public-read ACL on the bucket resource, so
	// an explicit public bucket cannot be honoured. Rather than silently downgrade
	// to private, surface a clean plan-time error (never an invented public toggle).
	if provider == ProviderStackIt && spec.Public {
		return ObjectStoragePlan{}, fmt.Errorf(
			"object-storage: StackIt object storage has no public-read ACL on the bucket "+
				"resource (stackit_objectstorage_bucket); a public bucket cannot be expressed. "+
				"Front it with stackit_cdn_distribution for public delivery, or keep it private "+
				"(public=false). This is a hard plan-time error, never a silent downgrade")
	}

	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = "pyxcloud-storage"
	}

	forceDestroy := false
	if spec.ForceDestroy != nil {
		forceDestroy = *spec.ForceDestroy
	}

	plan := ObjectStoragePlan{
		Provider:     provider,
		CSP:          row.CSP,
		RegionName:   row.RegionName,
		CSPRegion:    row.CSPRegion,
		BucketName:   deriveBucketName(name, row.CSP, row.CSPRegion),
		LogicalName:  name,
		Versioning:   spec.Versioning,
		Public:       spec.Public, // defaults false via validation; private-by-default
		ForceDestroy: forceDestroy,
	}

	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_s3_bucket"
	case ProviderGCP:
		plan.ResourceType = "google_storage_bucket"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_spaces_bucket"
	case ProviderStackIt:
		// StackIt buckets are private by default (no public-ACL toggle on the
		// resource), which matches PyxCloud's default-secure posture exactly.
		plan.ResourceType = "stackit_objectstorage_bucket"
	}
	return plan, nil
}

// deriveBucketName produces a globally-unique-safe, DNS-compatible bucket name.
//
// S3, GCS, and DO Spaces all share a GLOBAL (or per-provider-global) bucket
// namespace, so a bare "app-assets" would collide across accounts/regions. We
// derive deterministically:
//
//  1. lower-case the logical name and replace any char outside [a-z0-9-] with '-'
//     (DNS-bucket rules: lowercase letters, digits, hyphens; no underscores/dots),
//  2. trim leading/trailing hyphens and collapse the result to a valid prefix,
//  3. append a short hex hash of (csp|csp_region|name) so the SAME logical name in
//     two regions/providers yields DISTINCT global names (no cross-place clash),
//  4. clamp to the 63-char cross-provider limit (truncating the prefix, keeping
//     the full hash so uniqueness is preserved).
//
// The derivation is pure and stable: the same inputs always yield the same name,
// so plans are idempotent. It is documented here (and in the PR) as the naming
// contract.
func deriveBucketName(name, csp, cspRegion string) string {
	sanitised := sanitiseBucketPrefix(name)

	sum := sha256.Sum256([]byte(csp + "|" + cspRegion + "|" + name))
	suffix := hex.EncodeToString(sum[:])[:bucketHashLen]

	// Reserve room for "-" + suffix within the 63-char limit.
	maxPrefix := maxBucketNameLen - bucketHashLen - 1
	if len(sanitised) > maxPrefix {
		sanitised = strings.Trim(sanitised[:maxPrefix], "-")
	}
	if sanitised == "" {
		sanitised = "pyx"
	}
	return sanitised + "-" + suffix
}

// sanitiseBucketPrefix lower-cases and reduces s to the DNS-bucket charset
// [a-z0-9-], collapsing runs of invalid chars to a single hyphen and trimming
// leading/trailing hyphens.
func sanitiseBucketPrefix(s string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen {
				b.WriteRune('-')
				lastHyphen = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

func validateObjectStorageSpec(spec ObjectStorageSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("object-storage: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("object-storage: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("object-storage: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	return nil
}

// CanonicalObjectStorageType maps an accepted type token (object-storage /
// blob-storage) to the canonical object-storage token, reporting whether it is a
// recognised object/blob-storage type.
func CanonicalObjectStorageType(t string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case TypeObjectStorage, TypeBlobStorage:
		return TypeObjectStorage, true
	default:
		return "", false
	}
}
