package catalog

import (
	"context"
	"fmt"
	"strings"
)

// workloadidentity.go — the abstract `workload-identity` component
// (pd-MIG-WORKLOAD-IDENTITY).
//
// A workload identity is "a scoped credential a workload assumes at runtime,
// without baking long-lived static keys into the image". On AWS that is an IAM
// role attached via an instance profile (EC2) — the construct being migrated
// AWAY from. DigitalOcean has no IAM-role primitive, so the canonical
// replacement is a HashiCorp Vault identity:
//
//   - AWS (the peer we keep): aws_iam_role (assume_role_policy for the workload's
//     service principal) + inline / managed policies + an aws_iam_instance_profile
//     so an EC2 instance / droplet-equivalent assumes the role with no static keys.
//   - DigitalOcean: a Vault identity, delivered one of two ways depending on where
//     the workload runs:
//       * "approle"   — for a plain droplet: a Vault AppRole. The droplet's
//         cloud-init `user_data` logs in with a response-wrapped SecretID
//         (injected out-of-band at boot) to fetch scoped, short-lived tokens.
//         No static cloud keys live on the droplet. Rendered as the Vault
//         approle auth-role + policy CRs (operator pattern via the Vault config
//         operator) PLUS the user_data the droplet runs.
//       * "kubernetes" — for a DOKS workload: a Kubernetes ServiceAccount the pod
//         mounts, bound to a Vault Kubernetes-auth role. The pod's projected SA
//         token is exchanged for a scoped Vault token. Rendered as the
//         ServiceAccount + the Vault auth-role / policy CRs.
//
// Both DO modes follow the operator pattern (operator.go): the Vault config
// operator (CORE, installed by the Vault-HA component / its Helm chart) owns the
// controller + CRDs; the EXTRA we render are the VaultAuth / VaultPolicy custom
// resources that scope the identity. The component depends on the RegionCatalog
// only (an identity is region/cluster-scoped — no sizing catalog).

// Canonical workload-identity type tokens. `workload-identity` is canonical;
// `instance-identity` and `workload-id` are accepted aliases.
const (
	TypeWorkloadIdentity = "workload-identity"
	TypeInstanceIdentity = "instance-identity"
	TypeWorkloadID       = "workload-id"
)

// Workload-identity delivery modes for the DigitalOcean (Vault) peer.
const (
	WIDeliveryAppRole    = "approle"    // droplet user_data + Vault AppRole
	WIDeliveryKubernetes = "kubernetes" // DOKS ServiceAccount + Vault k8s auth
)

// Vault config operator (CORE) chart — the controller that reconciles the
// VaultAuth / VaultStaticSecret / policy CRs we render as EXTRA. Installed by the
// Vault-HA component; the workload-identity EXTRA depends on the same operator.
// Pinned for a deterministic plan.
const (
	defaultWorkloadIdentityNS = "vault"
	vaultDefaultMount         = "auth/approle"
	vaultK8sMount             = "auth/kubernetes"
)

// WorkloadIdentitySpec is the abstract, provider-neutral workload identity.
type WorkloadIdentitySpec struct {
	Name     string
	Region   string // abstract pyx region_name (provider validation; identity is region-scoped)
	Provider string

	// AssumeService is the principal that assumes the identity on AWS
	// (e.g. "ec2.amazonaws.com"). Empty -> "ec2.amazonaws.com" (the instance role
	// default). Ignored on DO.
	AssumeService string
	// InlinePolicies are scoped permission documents. On AWS they are raw IAM JSON
	// (the canonical policy form). On DO each becomes a Vault policy (its Document
	// carries the Vault HCL policy body, or the IAM JSON is carried verbatim for
	// the operator to translate — see RenderWorkloadIdentityHCL).
	InlinePolicies []IAMPolicy
	// ManagedPolicyARNs are AWS managed-policy ARNs to attach. AWS-only; on DO they
	// are a hard plan-time error (no managed-ARN concept in Vault) — never silently
	// dropped.
	ManagedPolicyARNs []string

	// ── DigitalOcean (Vault) ──
	// DeliveryMode selects how the Vault identity reaches the workload:
	// "approle" (droplet user_data) or "kubernetes" (DOKS ServiceAccount).
	// Empty -> "approle". Ignored on AWS.
	DeliveryMode string
	// ClusterName is the DOKS cluster the EXTRA CRs (+ ServiceAccount in k8s mode)
	// land on. Required for DO; ignored on AWS.
	ClusterName string
	// Namespace is the Kubernetes namespace for the CRs / ServiceAccount.
	// Empty -> "vault".
	Namespace string
	// VaultRole is the Vault auth role name the identity binds to. Empty -> Name.
	VaultRole string
	// TokenTTL bounds the lifetime of the scoped token the workload receives
	// (e.g. "1h"). Empty -> "1h" — short-lived by default, never a long-lived key.
	TokenTTL string
}

// WorkloadIdentityPlan is the resolved concrete translation.
type WorkloadIdentityPlan struct {
	Provider   string `json:"provider"`
	CSP        string `json:"csp"`
	RegionName string `json:"region_name"`
	CSPRegion  string `json:"csp_region"`
	Name       string `json:"name"`

	// ── AWS (IAM role + instance profile) ──
	AssumeService     string      `json:"assume_service,omitempty"`
	InlinePolicies    []IAMPolicy `json:"inline_policies,omitempty"`
	ManagedPolicyARNs []string    `json:"managed_policy_arns,omitempty"`

	// ── DigitalOcean (Vault) ──
	DeliveryMode string `json:"delivery_mode,omitempty"`
	ClusterName  string `json:"cluster_name,omitempty"`
	Namespace    string `json:"namespace,omitempty"`
	VaultRole    string `json:"vault_role,omitempty"`
	TokenTTL     string `json:"token_ttl,omitempty"`

	// RendersHelm is true when the render emits a helm_release (none today: the
	// Vault config operator CORE is owned by the Vault-HA component, so the
	// workload-identity render only emits the EXTRA CRs). Present for symmetry with
	// the other operator-pattern plans / the assembler pin logic.
	RendersHelm bool `json:"renders_helm,omitempty"`

	ResourceType string `json:"resource_type"` // top provider resource
}

// TranslateWorkloadIdentity resolves a WorkloadIdentitySpec. Deterministic and
// catalog-driven (csp_region from the region catalog, never invented). DO maps to
// Vault; AWS keeps the IAM-role + instance-profile peer. Unsupported providers /
// missing catalog data surface as hard plan-time errors, never silent fallbacks.
func TranslateWorkloadIdentity(ctx context.Context, cat RegionCatalog, spec WorkloadIdentitySpec) (WorkloadIdentityPlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return WorkloadIdentityPlan{}, fmt.Errorf("workload-identity: name is required")
	}
	if strings.TrimSpace(spec.Region) == "" {
		return WorkloadIdentityPlan{}, fmt.Errorf("workload-identity: region (abstract pyx region_name) is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return WorkloadIdentityPlan{}, fmt.Errorf("workload-identity: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return WorkloadIdentityPlan{}, err
	}
	provider := lc(spec.Provider)
	plan := WorkloadIdentityPlan{
		Provider:   provider,
		CSP:        csp,
		RegionName: row.RegionName,
		CSPRegion:  row.CSPRegion,
		Name:       canonicalName(spec.Name, "pyxcloud-workload-identity"),
	}
	switch provider {
	case ProviderAWS:
		// The IAM role + instance profile — the construct being migrated away from.
		assume := strings.TrimSpace(spec.AssumeService)
		if assume == "" {
			assume = "ec2.amazonaws.com"
		}
		plan.AssumeService = assume
		plan.InlinePolicies = spec.InlinePolicies
		plan.ManagedPolicyARNs = spec.ManagedPolicyARNs
		plan.ResourceType = "aws_iam_role"
	case ProviderDigitalOcean:
		// DO has no IAM-role primitive — the canonical replacement is a Vault
		// identity (AppRole on a droplet, or k8s-auth on DOKS). This is a hard
		// requirement, never a silent fallback to static keys.
		if len(spec.ManagedPolicyARNs) > 0 {
			return WorkloadIdentityPlan{}, fmt.Errorf(
				"workload-identity: managed_policy_arns are AWS-shaped and have no Vault " +
					"equivalent — declare scoped permissions as inline Vault policies instead " +
					"(hard plan-time error, never a silent drop)")
		}
		cluster := strings.TrimSpace(spec.ClusterName)
		if cluster == "" {
			return WorkloadIdentityPlan{}, fmt.Errorf(
				"workload-identity: digitalocean replaces the AWS IAM role with a HashiCorp Vault " +
					"identity (DO has no IAM-role primitive); the Vault auth-role / policy custom " +
					"resources run on a DOKS cluster — cluster_name is required. This is a hard " +
					"plan-time error, never a silent fallback to static keys")
		}
		mode := lc(spec.DeliveryMode)
		if mode == "" {
			mode = WIDeliveryAppRole
		}
		if mode != WIDeliveryAppRole && mode != WIDeliveryKubernetes {
			return WorkloadIdentityPlan{}, fmt.Errorf(
				"workload-identity: unknown delivery_mode %q (approle = droplet user_data | "+
					"kubernetes = DOKS ServiceAccount)", spec.DeliveryMode)
		}
		ns := strings.TrimSpace(spec.Namespace)
		if ns == "" {
			ns = defaultWorkloadIdentityNS
		}
		role := strings.TrimSpace(spec.VaultRole)
		if role == "" {
			role = plan.Name
		}
		ttl := strings.TrimSpace(spec.TokenTTL)
		if ttl == "" {
			ttl = "1h"
		}
		plan.DeliveryMode = mode
		plan.ClusterName = cluster
		plan.Namespace = ns
		plan.VaultRole = role
		plan.TokenTTL = ttl
		plan.InlinePolicies = spec.InlinePolicies
		plan.ResourceType = "kubernetes_manifest"
	default:
		return WorkloadIdentityPlan{}, ErrComponentUnsupported{
			Component: TypeWorkloadIdentity, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "workload-identity is supported on aws (aws_iam_role + instance profile) and " +
				"digitalocean (a HashiCorp Vault AppRole/Kubernetes-auth identity on DOKS); for other " +
				"providers run Vault and bind the workload to a Vault auth role",
		}
	}
	return plan, nil
}

// CanonicalWorkloadIdentityType maps an accepted type token to the canonical
// workload-identity token, reporting whether it is recognised.
func CanonicalWorkloadIdentityType(t string) (string, bool) {
	switch lc(t) {
	case TypeWorkloadIdentity, TypeInstanceIdentity, TypeWorkloadID:
		return TypeWorkloadIdentity, true
	default:
		return "", false
	}
}
