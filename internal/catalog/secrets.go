package catalog

import (
	"context"
	"fmt"
	"strings"
)

// SecretsManager is the abstract `secrets-manager` component (SPEC §5.8): a
// managed secret store entry.
//
//   - AWS: aws_secretsmanager_secret (+ optional rotation; KMS-encrypted).
//   - GCP: google_secret_manager_secret (automatic replication, Google-managed
//     encryption).
//   - DigitalOcean: UNSUPPORTED. DO has no managed secrets-manager primitive.
//     Clean plan-time error -> use AWS Secrets Manager / GCP Secret Manager, or
//     a self-hosted Vault. (DO App Platform env vars are not a secrets manager.)
//
// SECURITY: a secret VALUE is never declared in the canonical topology / Terraform
// state (that would leak it into state files). The component creates the secret
// CONTAINER (and rotation policy); the value is written out-of-band (CI / Vault /
// the console). This mirrors how the managed-database password is handled.

// SecretsSpec is the abstract secrets-manager description. Provider-neutral.
type SecretsSpec struct {
	Name     string
	Region   string
	Provider string

	// Description is a human note about the secret (ASCII-sanitised, like the SG
	// description — AWS rejects non-ASCII).
	Description string
	// RotationDays, when > 0, requests automatic rotation every N days (AWS native;
	// GCP carries it as the rotation period). 0 -> no automatic rotation.
	RotationDays int

	// ForceDestroy allows an immediate, recovery-window-free delete on destroy
	// (AWS recovery_window_in_days = 0). Defaults to false (production-safe: AWS's
	// default 30-day recovery window is kept). Pointer so an unset value takes the
	// safe default. The TEST round-trip sets it true ONLY so a just-created secret
	// tears down cleanly and the name is immediately reusable — that override is
	// test-only and visible in the fixture.
	ForceDestroy *bool
}

// SecretsPlan is the catalog-resolved concrete secrets-manager translation.
type SecretsPlan struct {
	Provider     string `json:"provider"`
	CSP          string `json:"csp"`
	RegionName   string `json:"region_name"`
	CSPRegion    string `json:"csp_region"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	RotationDays int    `json:"rotation_days"`
	ForceDestroy bool   `json:"force_destroy"`
	ResourceType string `json:"resource_type"`
}

// TranslateSecrets resolves a SecretsSpec. DO is a clean unsupported error.
func TranslateSecrets(ctx context.Context, cat RegionCatalog, spec SecretsSpec) (SecretsPlan, error) {
	if strings.TrimSpace(spec.Region) == "" {
		return SecretsPlan{}, fmt.Errorf("secrets-manager: region (abstract pyx region_name) is required")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return SecretsPlan{}, fmt.Errorf("secrets-manager: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if spec.RotationDays < 0 {
		return SecretsPlan{}, fmt.Errorf("secrets-manager: rotation_days must be >= 0 (0 = no rotation)")
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return SecretsPlan{}, err
	}
	provider := lc(spec.Provider)
	if provider == ProviderDigitalOcean || provider == ProviderLinode {
		provName := "DigitalOcean"
		envNote := "DO App Platform env vars are not a secrets manager"
		if provider == ProviderLinode {
			provName = "Linode"
			envNote = "Linode has no first-party secrets manager"
		}
		return SecretsPlan{}, ErrComponentUnsupported{
			Component: TypeSecretsManager, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: provName + " has no managed secrets-manager primitive; use AWS Secrets Manager " +
				"or GCP Secret Manager, or run self-hosted HashiCorp Vault on a virtual-machine " +
				"(" + envNote + ")",
		}
	}
	forceDestroy := false
	if spec.ForceDestroy != nil {
		forceDestroy = *spec.ForceDestroy
	}
	plan := SecretsPlan{
		Provider:     provider,
		CSP:          row.CSP,
		RegionName:   row.RegionName,
		CSPRegion:    row.CSPRegion,
		Name:         canonicalName(spec.Name, "pyxcloud-secret"),
		Description:  asciiOnly(spec.Description),
		RotationDays: spec.RotationDays,
		ForceDestroy: forceDestroy,
	}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_secretsmanager_secret"
	case ProviderGCP:
		plan.ResourceType = "google_secret_manager_secret"
	case ProviderAzure:
		plan.ResourceType = "azurerm_key_vault"
	case ProviderOracle:
		plan.ResourceType = "oci_vault_secret"
	}
	return plan, nil
}

// CanonicalSecretsType reports whether t names the secrets-manager component.
func CanonicalSecretsType(t string) (string, bool) {
	if lc(t) == TypeSecretsManager {
		return TypeSecretsManager, true
	}
	return "", false
}
