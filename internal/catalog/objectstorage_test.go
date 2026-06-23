package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// osBoolPtr is a test helper for the *bool override field.
func osBoolPtr(b bool) *bool { return &b }

// TestTranslateObjectStorageAWS asserts the resolved structured plan for AWS:
// catalog-resolved csp_region, a globally-unique-safe bucket name, private by
// default (Public=false), versioning carried, and the aws_s3_bucket resource type.
func TestTranslateObjectStorageAWS(t *testing.T) {
	t.Parallel()
	// Frankfurt -> eu-central-1 (AWS).
	plan, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name:       "app-assets",
		Region:     "Frankfurt",
		Provider:   "aws",
		Versioning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_s3_bucket" {
		t.Errorf("resource_type = %q, want aws_s3_bucket", plan.ResourceType)
	}
	if plan.Public {
		t.Error("bucket must be PRIVATE by default (public should be false)")
	}
	if !plan.Versioning {
		t.Error("versioning should be carried into the plan")
	}
	if plan.ForceDestroy {
		t.Error("force_destroy should default to false (production-safe)")
	}
	if !strings.HasPrefix(plan.BucketName, "app-assets-") {
		t.Errorf("bucket name %q should start with the sanitised logical name", plan.BucketName)
	}
	if plan.LogicalName != "app-assets" {
		t.Errorf("logical_name = %q, want app-assets", plan.LogicalName)
	}
}

func TestTranslateObjectStorageGCP(t *testing.T) {
	t.Parallel()
	// Frankfurt -> europe-west3 (GCP).
	plan, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name: "app-assets", Region: "Frankfurt", Provider: "gcp", Versioning: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "europe-west3" {
		t.Errorf("csp_region = %q, want europe-west3", plan.CSPRegion)
	}
	if plan.ResourceType != "google_storage_bucket" {
		t.Errorf("resource_type = %q, want google_storage_bucket", plan.ResourceType)
	}
	if plan.Public {
		t.Error("bucket must be PRIVATE by default")
	}
}

func TestTranslateObjectStorageDO(t *testing.T) {
	t.Parallel()
	// Frankfurt -> fra1 (DO Spaces region).
	plan, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name: "app-assets", Region: "Frankfurt", Provider: "digitalocean",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", plan.CSPRegion)
	}
	if plan.ResourceType != "digitalocean_spaces_bucket" {
		t.Errorf("resource_type = %q, want digitalocean_spaces_bucket", plan.ResourceType)
	}
	if plan.Public {
		t.Error("bucket must be PRIVATE by default")
	}
}

// TestObjectStorageBlobAlias asserts the blob-storage alias canonicalises.
func TestObjectStorageTypeAlias(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"object-storage", "blob-storage", "OBJECT-STORAGE", " blob-storage "} {
		got, ok := CanonicalObjectStorageType(in)
		if !ok {
			t.Errorf("%q should be a recognised object/blob-storage type", in)
		}
		if got != TypeObjectStorage {
			t.Errorf("%q -> %q, want %q", in, got, TypeObjectStorage)
		}
	}
	if _, ok := CanonicalObjectStorageType("virtual-machine"); ok {
		t.Error("virtual-machine should not be an object-storage type")
	}
}

// TestObjectStorageBucketNameUniqueness asserts the derived bucket name is
// globally-unique-safe: the SAME logical name in different regions/providers
// yields DISTINCT names, and the derivation is deterministic and within limits.
func TestObjectStorageBucketNameUniqueness(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	mk := func(region, provider string) string {
		p, err := TranslateObjectStorage(context.Background(), cat, ObjectStorageSpec{
			Name: "shared", Region: region, Provider: provider,
		})
		if err != nil {
			t.Fatal(err)
		}
		return p.BucketName
	}
	awsFra := mk("Frankfurt", "aws")
	awsDub := mk("Dublin", "aws")
	gcpFra := mk("Frankfurt", "gcp")

	if awsFra == awsDub {
		t.Errorf("same logical name in different regions must differ: %q == %q", awsFra, awsDub)
	}
	if awsFra == gcpFra {
		t.Errorf("same logical name on different providers must differ: %q == %q", awsFra, gcpFra)
	}
	// Deterministic: a second translation yields the same name.
	if again := mk("Frankfurt", "aws"); again != awsFra {
		t.Errorf("derivation must be deterministic: %q != %q", again, awsFra)
	}
	for _, n := range []string{awsFra, awsDub, gcpFra} {
		if len(n) > maxBucketNameLen {
			t.Errorf("bucket name %q exceeds %d-char limit", n, maxBucketNameLen)
		}
		if !isDNSBucketSafe(n) {
			t.Errorf("bucket name %q is not DNS-bucket-safe", n)
		}
	}
}

// TestObjectStorageBucketNameLongInput asserts a long/dirty logical name is
// sanitised and clamped, still keeping the uniqueness hash.
func TestObjectStorageBucketNameLongInput(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("My_Very.Long Bucket NAME!! ", 5)
	name := deriveBucketName(long, "aws", "eu-central-1")
	if len(name) > maxBucketNameLen {
		t.Errorf("derived name %q exceeds %d chars", name, maxBucketNameLen)
	}
	if !isDNSBucketSafe(name) {
		t.Errorf("derived name %q is not DNS-bucket-safe", name)
	}
	// The hash suffix (10 hex chars) is preserved at the tail.
	if len(name) < bucketHashLen+1 {
		t.Errorf("derived name %q lost its uniqueness suffix", name)
	}
}

func isDNSBucketSafe(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
			return false
		}
	}
	return true
}

// TestObjectStorageRegionNotFound asserts an unresolvable region is a hard error.
func TestObjectStorageRegionNotFound(t *testing.T) {
	t.Parallel()
	_, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name: "x", Region: "Atlantis", Provider: "aws",
	})
	if err == nil {
		t.Fatal("expected region-not-found error, got nil")
	}
	var nf ErrRegionNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("expected ErrRegionNotFound, got %T: %v", err, err)
	}
}

func TestObjectStorageValidation(t *testing.T) {
	t.Parallel()
	cat := MustEmbedded()
	cases := []struct {
		name string
		spec ObjectStorageSpec
	}{
		{"missing region", ObjectStorageSpec{Provider: "aws"}},
		{"missing provider", ObjectStorageSpec{Region: "Frankfurt"}},
		{"unknown provider", ObjectStorageSpec{Region: "Frankfurt", Provider: "vultr"}},
	}
	for _, c := range cases {
		if _, err := TranslateObjectStorage(context.Background(), cat, c.spec); err == nil {
			t.Errorf("%s: expected validation error, got nil", c.name)
		}
	}
}

// ── RENDER TESTS ─────────────────────────────────────────────────────────────

// osPlan builds a baseline resolved plan for the render tests.
func osPlan(t *testing.T, spec ObjectStorageSpec) ObjectStoragePlan {
	t.Helper()
	if spec.Region == "" {
		spec.Region = "Frankfurt"
	}
	if spec.Provider == "" {
		spec.Provider = "aws"
	}
	if spec.Name == "" {
		spec.Name = "app-assets"
	}
	p, err := TranslateObjectStorage(context.Background(), MustEmbedded(), spec)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRenderObjectStorageAWSPrivateDefault asserts the S3 shaping AND the
// DEFAULT-PRIVATE / public-access-block enforcement (SPEC §5.7): when not public,
// all four block flags are true and versioning is Enabled.
func TestRenderObjectStorageAWSPrivateDefault(t *testing.T) {
	t.Parallel()
	plan := osPlan(t, ObjectStorageSpec{Name: "app-assets", Versioning: true})
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_s3_bucket" "app-assets"`,
		`bucket        = "` + plan.BucketName + `"`,
		`force_destroy = false`,
		`resource "aws_s3_bucket_versioning" "app-assets"`,
		`status = "Enabled"`,
		`resource "aws_s3_bucket_public_access_block" "app-assets"`,
		`block_public_acls       = true`,
		`block_public_policy     = true`,
		`ignore_public_acls      = true`,
		`restrict_public_buckets = true`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws object-storage HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL is not ASCII:\n%s", hcl)
	}
}

// TestRenderObjectStorageAWSPublicOptIn asserts that an explicit public bucket
// disables the access block (opt-in only) — proving the default is the SECURE one.
func TestRenderObjectStorageAWSPublicOptIn(t *testing.T) {
	t.Parallel()
	plan := osPlan(t, ObjectStorageSpec{Name: "app-assets", Public: true})
	hcl, _ := RenderObjectStorageHCL(plan)
	for _, want := range []string{
		`block_public_acls       = false`,
		`restrict_public_buckets = false`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("public bucket should disable the access-block flag %q:\n%s", want, hcl)
		}
	}
}

// TestRenderObjectStorageAWSVersioningSuspended asserts versioning=false emits
// Suspended (explicit, idempotent).
func TestRenderObjectStorageAWSVersioningSuspended(t *testing.T) {
	t.Parallel()
	plan := osPlan(t, ObjectStorageSpec{Versioning: false})
	hcl, _ := RenderObjectStorageHCL(plan)
	if !strings.Contains(hcl, `status = "Suspended"`) {
		t.Errorf("versioning=false should emit Suspended:\n%s", hcl)
	}
}

// TestRenderObjectStorageAWSForceDestroyOverride asserts the test-only override
// emits force_destroy = true (clean teardown of a just-created bucket).
func TestRenderObjectStorageAWSForceDestroyOverride(t *testing.T) {
	t.Parallel()
	plan := osPlan(t, ObjectStorageSpec{Name: "app-assets", ForceDestroy: osBoolPtr(true)})
	hcl, _ := RenderObjectStorageHCL(plan)
	if !strings.Contains(hcl, `force_destroy = true`) {
		t.Errorf("force_destroy override should emit true:\n%s", hcl)
	}
}

func TestRenderObjectStorageGCP(t *testing.T) {
	t.Parallel()
	plan := osPlan(t, ObjectStorageSpec{Name: "app-assets", Provider: "gcp", Versioning: true})
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "google_storage_bucket" "app-assets"`,
		`location      = "EUROPE-WEST3"`,
		`uniform_bucket_level_access = true`,
		`public_access_prevention    = "enforced"`,
		`enabled = true`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("gcp object-storage HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestRenderObjectStorageGCPPublicOptIn asserts a public GCP bucket drops the
// enforced public_access_prevention (still uniform).
func TestRenderObjectStorageGCPPublicOptIn(t *testing.T) {
	t.Parallel()
	plan := osPlan(t, ObjectStorageSpec{Provider: "gcp", Public: true})
	hcl, _ := RenderObjectStorageHCL(plan)
	if strings.Contains(hcl, `public_access_prevention    = "enforced"`) {
		t.Errorf("public bucket should not enforce public_access_prevention:\n%s", hcl)
	}
	if !strings.Contains(hcl, `uniform_bucket_level_access = true`) {
		t.Errorf("uniform access should remain on:\n%s", hcl)
	}
}

func TestRenderObjectStorageDO(t *testing.T) {
	t.Parallel()
	plan := osPlan(t, ObjectStorageSpec{Name: "app-assets", Provider: "digitalocean", Versioning: true})
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "digitalocean_spaces_bucket" "app-assets"`,
		`region        = "fra1"`,
		`acl           = "private"`,
		`enabled = true`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do object-storage HCL missing %q:\n%s", want, hcl)
		}
	}
}

// TestRenderObjectStorageDOPublicOptIn asserts a public DO bucket uses public-read.
func TestRenderObjectStorageDOPublicOptIn(t *testing.T) {
	t.Parallel()
	plan := osPlan(t, ObjectStorageSpec{Provider: "digitalocean", Public: true})
	hcl, _ := RenderObjectStorageHCL(plan)
	if !strings.Contains(hcl, `acl           = "public-read"`) {
		t.Errorf("public DO bucket should use public-read acl:\n%s", hcl)
	}
}

// TestRenderObjectStorageUnsupportedProvider asserts the renderer rejects an
// unknown provider (defence in depth for a hand-built plan).
func TestRenderObjectStorageUnsupportedProvider(t *testing.T) {
	t.Parallel()
	if _, err := RenderObjectStorageHCL(ObjectStoragePlan{Provider: "vultr"}); err == nil {
		t.Fatal("expected render error for unsupported provider, got nil")
	}
}

// ── pd-MIG-OBJSTORE-PARITY: lifecycle + SSE + bucket-policy + access-logs ──────

// paritySpec is a full-parity spec used by the parity tests.
func paritySpec(provider string) ObjectStorageSpec {
	return ObjectStorageSpec{
		Name: "app-assets", Region: "Frankfurt", Provider: provider, Versioning: true,
		Lifecycle: []LifecycleRule{
			// Deliberately out of ID order to prove deterministic sorting.
			{ID: "expire-tmp", Prefix: "tmp/", Enabled: true, ExpireDays: 30},
			{ID: "abort-mpu", Enabled: true, AbortIncompleteMultipartDays: 7},
			{ID: "prune-old-versions", Enabled: true, NoncurrentVersionExpireDays: 90},
		},
		SSE:          &SSEConfig{Algorithm: SSEAlgoAES256},
		BucketPolicy: `{"Version":"2012-10-17","Statement":[]}`,
		AccessLogs:   &AccessLogConfig{TargetBucket: "audit-logs", TargetPrefix: "s3/"},
	}
}

// TestObjectStorageParityLifecycleSortedAndValidated asserts lifecycle rules are
// canonicalised (sorted by id) and the no-action / duplicate / missing-id cases
// are hard errors.
func TestObjectStorageParityLifecycleSortedAndValidated(t *testing.T) {
	t.Parallel()
	plan, err := TranslateObjectStorage(context.Background(), MustEmbedded(), paritySpec("aws"))
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := []string{plan.Lifecycle[0].ID, plan.Lifecycle[1].ID, plan.Lifecycle[2].ID}
	want := []string{"abort-mpu", "expire-tmp", "prune-old-versions"}
	for i := range want {
		if gotIDs[i] != want[i] {
			t.Errorf("lifecycle not sorted by id: got %v want %v", gotIDs, want)
			break
		}
	}
	bad := []ObjectStorageSpec{
		{Name: "x", Region: "Frankfurt", Provider: "aws", Lifecycle: []LifecycleRule{{ID: "", ExpireDays: 1}}},
		{Name: "x", Region: "Frankfurt", Provider: "aws", Lifecycle: []LifecycleRule{{ID: "noop"}}},
		{Name: "x", Region: "Frankfurt", Provider: "aws", Lifecycle: []LifecycleRule{{ID: "d", ExpireDays: 1}, {ID: "d", ExpireDays: 2}}},
	}
	for i, b := range bad {
		if _, err := TranslateObjectStorage(context.Background(), MustEmbedded(), b); err == nil {
			t.Errorf("bad lifecycle case %d should error", i)
		}
	}
}

// TestObjectStorageParitySSEKMSOnDOIsHardError proves SSE-KMS on DO is a hard
// plan-time error (never a silent downgrade to AES256), and AES256 is fine on DO.
func TestObjectStorageParitySSEKMSOnDOIsHardError(t *testing.T) {
	t.Parallel()
	_, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean",
		SSE: &SSEConfig{Algorithm: SSEAlgoKMS, KMSKeyID: "arn:aws:kms:::key/abc"},
	})
	if err == nil || !strings.Contains(err.Error(), "AWS-only") {
		t.Fatalf("SSE-KMS on DO must be a hard error, got %v", err)
	}
	// AES256 on DO resolves cleanly.
	if _, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name: "x", Region: "Frankfurt", Provider: "digitalocean", SSE: &SSEConfig{Algorithm: SSEAlgoAES256},
	}); err != nil {
		t.Fatalf("AES256 on DO should resolve: %v", err)
	}
	// AES256 + a KMS key id is contradictory -> error.
	if _, err := TranslateObjectStorage(context.Background(), MustEmbedded(), ObjectStorageSpec{
		Name: "x", Region: "Frankfurt", Provider: "aws", SSE: &SSEConfig{Algorithm: SSEAlgoAES256, KMSKeyID: "k"},
	}); err == nil {
		t.Error("AES256 + kms_key_id should be a validation error")
	}
}

// TestRenderObjectStorageParityAWS asserts the AWS peer emits all four parity
// sub-resources with the right shaping.
func TestRenderObjectStorageParityAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateObjectStorage(context.Background(), MustEmbedded(), paritySpec("aws"))
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_s3_bucket_server_side_encryption_configuration" "app-assets"`,
		`sse_algorithm     = "AES256"`,
		`resource "aws_s3_bucket_lifecycle_configuration" "app-assets"`,
		`id     = "abort-mpu"`,
		`days_after_initiation = 7`,
		`noncurrent_days = 90`,
		`days = 30`,
		`prefix = "tmp/"`,
		`resource "aws_s3_bucket_policy" "app-assets"`,
		`resource "aws_s3_bucket_logging" "app-assets"`,
		`target_bucket = "audit-logs"`,
		`target_prefix = "s3/"`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("aws parity HCL missing %q:\n%s", want, hcl)
		}
	}
	if !IsASCII(hcl) {
		t.Errorf("rendered HCL not ASCII:\n%s", hcl)
	}
}

// TestRenderObjectStorageParityDO asserts DO Spaces carries lifecycle (inline)
// and bucket-policy, notes SSE-at-rest, and flags the access-log gap.
func TestRenderObjectStorageParityDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateObjectStorage(context.Background(), MustEmbedded(), paritySpec("digitalocean"))
	if err != nil {
		t.Fatal(err)
	}
	hcl, err := RenderObjectStorageHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "digitalocean_spaces_bucket" "app-assets"`,
		`lifecycle_rule {`,
		`id      = "abort-mpu"`,
		`abort_incomplete_multipart_upload_days = 7`,
		`prefix  = "tmp/"`,
		`# server-side encryption (AES256) is enabled at rest by default`,
		`resource "digitalocean_spaces_bucket_policy" "app-assets"`,
		`# NOTE: server access logging`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("do parity HCL missing %q:\n%s", want, hcl)
		}
	}
}
