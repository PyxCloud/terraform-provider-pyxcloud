package catalog

import (
	"context"
	"fmt"
	"strings"
)

// vaultha.go — the abstract `vault-ha` component (pd-MIG-VAULT-HA-HARDEN).
//
// Vault-HA is the enterprise-grade secrets/KMS replacement: a HashiCorp Vault
// cluster in HA (Raft integrated storage) with Transit auto-unseal. It is the
// AWS-Secrets-Manager + KMS migration target, and the CORE the workload-identity
// component's Vault auth roles bind to.
//
// MANDATORY operator pattern (KB managed-service-replacements-as-operators /
// SPEC §4.1, operator.go):
//
//   - CORE (maintained upstream): the OFFICIAL HashiCorp Vault Helm chart
//     (hashicorp/vault), installed in HA Raft mode as a `helm_release`. The chart
//     owns the StatefulSet, Raft peer config, RBAC and the Vault Secrets Operator
//     (VSO) controller + CRDs — we do NOT hand-roll the Vault Deployment/Service.
//   - EXTRA (maintained by us): the config custom resources the operator
//     reconciles — a VaultConnection (how the VSO reaches this cluster) and a
//     VaultAuthGlobal default per enabled auth method (approle / kubernetes), so a
//     workload-identity's per-role VaultAuth has a default to inherit. The Transit
//     auto-unseal key + seal config is set as chart values on the CORE release.
//
// Per-provider mapping:
//
//   - AWS (the peer we keep): aws_secretsmanager_secret + aws_kms_key — the managed
//     Secrets Manager / KMS pairing being migrated AWAY from. (KMS is also the AWS
//     way to seal/unseal, so it is the natural Transit-auto-unseal peer.)
//   - DigitalOcean: the Vault-HA operator-pattern stack on DOKS (DO has no managed
//     secrets/KMS service). CORE = the hashicorp/vault Helm chart (HA Raft +
//     Transit auto-unseal); EXTRA = VaultConnection + VaultAuthGlobal CRs.

// Canonical vault-ha type tokens.
const (
	TypeVaultHA      = "vault-ha"
	TypeVault        = "vault"
	TypeVaultCluster = "vault-cluster"
)

// Operator-pattern CORE chart for the DO Vault-HA stack. Pinned for a
// deterministic plan. The hashicorp/vault chart installs Vault (HA Raft) AND the
// Vault Secrets Operator (the controller + CRDs the EXTRA CRs target).
const (
	vaultChartRepo       = "https://helm.releases.hashicorp.com"
	vaultChart           = "vault"
	vaultDefaultVersion  = "0.28.1"
	defaultVaultNS       = "vault"
	defaultVaultReplicas = 3 // an odd Raft quorum; HA requires >= 3
	defaultTransitKey    = "autounseal"
)

// VaultHASpec is the abstract, provider-neutral Vault-HA description.
type VaultHASpec struct {
	Name     string
	Region   string // abstract pyx region_name
	Provider string

	// ── DigitalOcean (operator pattern) ──
	// ClusterName is the existing DOKS cluster Vault runs on. Required for DO;
	// ignored on AWS.
	ClusterName string
	// Namespace is the Kubernetes namespace for the Vault workloads. Empty -> "vault".
	Namespace string
	// Replicas is the Raft peer count. Empty/0 -> 3. HA requires an odd quorum >= 3;
	// an even or < 3 value is a hard plan-time error (never a silently degraded
	// single-node "HA").
	Replicas int
	// ChartVersion overrides the pinned Vault chart version (DO). Empty -> default.
	ChartVersion string
	// TransitUnseal enables Transit auto-unseal (recommended; avoids manual unseal
	// after every restart). Defaults to true on DO via the assembler — but a spec
	// that explicitly disables it is honoured.
	TransitUnseal bool
	// TransitKeyName is the Transit key used for auto-unseal. Empty -> "autounseal".
	TransitKeyName string
	// AuthMethods are the Vault auth backends to enable a default VaultAuthGlobal for
	// (approle | kubernetes), so workload-identity roles have a default to inherit.
	// Empty -> ["kubernetes", "approle"].
	AuthMethods []string
}

// VaultHAPlan is the resolved concrete Vault-HA translation.
type VaultHAPlan struct {
	Provider   string `json:"provider"`
	CSP        string `json:"csp"`
	RegionName string `json:"region_name"`
	CSPRegion  string `json:"csp_region"`
	Name       string `json:"name"`

	// ── DigitalOcean (operator pattern) ──
	ClusterName    string   `json:"cluster_name,omitempty"`
	Namespace      string   `json:"namespace,omitempty"`
	Replicas       int      `json:"replicas,omitempty"`
	ChartVersion   string   `json:"chart_version,omitempty"`
	TransitUnseal  bool     `json:"transit_unseal,omitempty"`
	TransitKeyName string   `json:"transit_key_name,omitempty"`
	AuthMethods    []string `json:"auth_methods,omitempty"`

	// RendersHelm is true when the render emits a helm_release (the operator-pattern
	// CORE — the official Vault Helm chart). assemble.go pins hashicorp/helm.
	RendersHelm bool `json:"renders_helm,omitempty"`

	ResourceType string `json:"resource_type"` // top provider resource
}

// TranslateVaultHA resolves a VaultHASpec. Deterministic and catalog-driven
// (csp_region from the region catalog). DO maps to the Vault HA operator stack;
// AWS keeps the Secrets Manager + KMS peer. Missing catalog data / unsupported
// providers surface as hard plan-time errors, never silent fallbacks.
func TranslateVaultHA(ctx context.Context, cat RegionCatalog, spec VaultHASpec) (VaultHAPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return VaultHAPlan{}, fmt.Errorf("vault-ha: name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return VaultHAPlan{}, fmt.Errorf("vault-ha: region (abstract pyx region_name) is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return VaultHAPlan{}, fmt.Errorf("vault-ha: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return VaultHAPlan{}, err
	}
	provider := lc(spec.Provider)
	plan := VaultHAPlan{
		Provider:   provider,
		CSP:        csp,
		RegionName: row.RegionName,
		CSPRegion:  row.CSPRegion,
		Name:       canonicalName(spec.Name, "pyxcloud-vault"),
	}
	switch provider {
	case ProviderAWS:
		// The managed Secrets Manager + KMS pairing being migrated away from. KMS is
		// also the AWS seal/unseal mechanism (the Transit-auto-unseal peer).
		plan.ResourceType = "aws_secretsmanager_secret"
	case ProviderDigitalOcean:
		cluster := strings.TrimSpace(spec.ClusterName)
		if cluster == "" {
			return VaultHAPlan{}, fmt.Errorf(
				"vault-ha: digitalocean replaces AWS Secrets Manager + KMS with a HashiCorp Vault HA " +
					"cluster (Raft) on a DOKS cluster (DO has no managed secrets/KMS service) — " +
					"cluster_name is required. This is a hard plan-time error, never a silent fallback")
		}
		replicas := spec.Replicas
		if replicas == 0 {
			replicas = defaultVaultReplicas
		}
		// HA = Raft quorum: require an odd count >= 3. A single-node or even-count
		// "HA" is not HA — reject it loudly rather than ship a false HA promise.
		if replicas < 3 {
			return VaultHAPlan{}, fmt.Errorf(
				"vault-ha: replicas=%d is not highly available — Raft HA needs an odd quorum of at "+
					"least 3 (this is a hard plan-time error, never a silently degraded single node)", replicas)
		}
		if replicas%2 == 0 {
			return VaultHAPlan{}, fmt.Errorf(
				"vault-ha: replicas=%d is an even Raft quorum (split-brain risk) — use an odd count "+
					"(3, 5, ...) for a stable leader election", replicas)
		}
		ns := strings.TrimSpace(spec.Namespace)
		if ns == "" {
			ns = defaultVaultNS
		}
		version := strings.TrimSpace(spec.ChartVersion)
		if version == "" {
			version = vaultDefaultVersion
		}
		transitKey := strings.TrimSpace(spec.TransitKeyName)
		if transitKey == "" {
			transitKey = defaultTransitKey
		}
		methods := normalizeAuthMethods(spec.AuthMethods)
		if err := validateAuthMethods(methods); err != nil {
			return VaultHAPlan{}, err
		}
		plan.ClusterName = cluster
		plan.Namespace = ns
		plan.Replicas = replicas
		plan.ChartVersion = version
		plan.TransitUnseal = spec.TransitUnseal
		plan.TransitKeyName = transitKey
		plan.AuthMethods = methods
		// Operator pattern: CORE = the official Vault Helm chart (helm_release);
		// EXTRA = VaultConnection + VaultAuthGlobal CRs (kubernetes_manifest).
		plan.RendersHelm = true
		plan.ResourceType = "kubernetes_manifest"
	default:
		return VaultHAPlan{}, ErrComponentUnsupported{
			Component: TypeVaultHA, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "vault-ha is supported on aws (Secrets Manager + KMS) and digitalocean " +
				"(a HashiCorp Vault HA Raft cluster + Transit auto-unseal on DOKS via the official " +
				"Vault Helm chart); for other providers run the Vault Helm chart on a managed-kubernetes cluster",
		}
	}
	return plan, nil
}

// normalizeAuthMethods lower-cases, dedups and applies the default auth-method
// set ([kubernetes, approle]) when none are supplied. Order is deterministic.
func normalizeAuthMethods(methods []string) []string {
	if len(methods) == 0 {
		return []string{WIDeliveryKubernetes, WIDeliveryAppRole}
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range methods {
		m = lc(m)
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		out = append(out, m)
	}
	return out
}

// validateAuthMethods rejects unknown auth methods loudly (never silently drop).
func validateAuthMethods(methods []string) error {
	for _, m := range methods {
		if m != WIDeliveryAppRole && m != WIDeliveryKubernetes {
			return fmt.Errorf("vault-ha: unknown auth_method %q (approle | kubernetes)", m)
		}
	}
	return nil
}

// CanonicalVaultHAType maps an accepted type token to the canonical vault-ha
// token, reporting whether it is recognised.
func CanonicalVaultHAType(t string) (string, bool) {
	switch lc(t) {
	case TypeVaultHA, TypeVault, TypeVaultCluster:
		return TypeVaultHA, true
	default:
		return "", false
	}
}
