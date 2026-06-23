package catalog

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// vpn-access is the abstract `vpn-access` topology signal: a component declares
// "this place needs corporate-VPN (WireGuard) access, gated Just-In-Time by
// Keycloak", and the provider auto-wires the JIT door instead of the manual
// PyxCloud/internal-vpn `add-peer.sh` + hand-written `jit-backing/terraform.tf`.
//
// WHAT THE SIGNAL REPLACES (memory: internal-vpn-repo, vpn-network-hardening-design):
// the JIT door is three coupled AWS pieces that today are stitched by hand:
//   1. A dedicated WireGuard "wg-jit" security group — UDP 51820, ingress EMPTY
//      at rest and OWNED BY THE KEYCLOAK SPI at runtime (the SPI opens 51820 to a
//      logged-in user's source IP /32 on login, revokes on logout/idle). Terraform
//      owns the SG but must `ignore_changes = [ingress]` so the SPI and terraform
//      never fight. Optional break-glass CIDRs (admin lockout safety).
//   2. A DynamoDB `jit-allowlist` table (hash key sessionId, TTL on ttlEpoch, PITR)
//      — the SPI's session->IP-rule backing store the GC reaper sweeps.
//   3. An IAM policy granting the Keycloak instance role exactly the SG-ingress +
//      DynamoDB actions the SPI needs, attached to that role.
//
// This is an AWS-native pattern (WireGuard + a Keycloak EC2 SPI mutating an AWS SG
// + DynamoDB). It maps to no other provider's managed primitive, so — per the
// abstract-first / never-invent ethos (SPEC §1, §4) — every non-AWS provider gets a
// clean ErrComponentUnsupported, never a fabricated resource.
//
// Catalog dependency: region only (region_name + provider -> csp_region). The JIT
// door has no sizing/price table (a SG + a PAY_PER_REQUEST DynamoDB table + an IAM
// policy are flat/region-scoped), exactly like reserved-ip / container-registry.

// Canonical vpn-access type token. `vpn-access` is canonical; `jit-access` and
// `vpn-door` are accepted aliases (all name the same JIT-door signal).
const (
	TypeVPNAccess = "vpn-access"
	TypeJITAccess = "jit-access"
	TypeVPNDoor   = "vpn-door"

	// defaultWireGuardPort is the WireGuard UDP port the JIT door opens. 51820 is
	// the WireGuard default and what the internal-vpn server listens on.
	defaultWireGuardPort = 51820
	// defaultJITAllowlistTable is the DynamoDB table the Keycloak SPI uses as its
	// session->ingress-rule backing store (matches internal-vpn/jit-backing).
	defaultJITAllowlistTable = "jit-allowlist"
)

// VPNAccessSpec is the abstract description of the JIT VPN door for a place — the
// canonical `vpn-access { name, keycloak_role, wireguard_port, break_glass_cidrs,
// allowlist_table }`. Provider-neutral; AWS is the only target today.
type VPNAccessSpec struct {
	Name     string // signal/component name, e.g. "vpn"
	Region   string // abstract pyx region_name, e.g. "Frankfurt"
	Provider string // provider-facing name: aws (others -> unsupported)

	// VPC selects the network the wg-jit security group attaches to. Optional: when
	// empty the renderer falls back to the account default VPC (data.aws_vpc.default),
	// matching the other env components' VPC handling.
	VPC string

	// KeycloakRole is the IAM role NAME of the Keycloak EC2/ASG that runs the JIT
	// SPI. The generated IAM policy (SG-ingress + DynamoDB) is attached to it so the
	// SPI can open/close the door. Required: the door is inert without a writer.
	KeycloakRole string

	// WireGuardPort is the UDP port the door gates (default 51820).
	WireGuardPort int

	// BreakGlassCIDRs are optional CIDRs allowed to reach the WireGuard port
	// regardless of JIT (admin lockout safety). Empty = pure JIT (dark at rest).
	BreakGlassCIDRs []string

	// AllowlistTable overrides the DynamoDB table name (default "jit-allowlist").
	AllowlistTable string

	// PointInTimeRecovery enables DynamoDB PITR on the allowlist table (default
	// true — the table is the access-control source of truth).
	PointInTimeRecovery *bool
}

// VPNAccessPlan is the deterministic, catalog-resolved concrete translation of a
// VPNAccessSpec for one provider. STRUCTURED plan (not rendered .tf) — the provider
// owns rendering and state, consistent with the other components (SPEC §8).
type VPNAccessPlan struct {
	Provider   string `json:"provider"`    // aws
	CSP        string `json:"csp"`         // catalog token: aws
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)

	LogicalName     string   `json:"logical_name"`      // user's abstract name (tf resource label)
	VPC             string   `json:"vpc"`               // network the wg-jit SG attaches to ("" = default VPC)
	KeycloakRole    string   `json:"keycloak_role"`     // IAM role name the JIT policy is attached to
	WireGuardPort   int      `json:"wireguard_port"`    // UDP port the door gates
	BreakGlassCIDRs []string `json:"break_glass_cidrs"` // optional static allow CIDRs
	AllowlistTable  string   `json:"allowlist_table"`   // DynamoDB table name
	PITR            bool     `json:"pitr"`              // DynamoDB point-in-time recovery

	// SGResourceType is the top provider security-group resource (the JIT door SG).
	SGResourceType string `json:"sg_resource_type"`
	// TableResourceType is the provider key-value table resource (the allowlist).
	TableResourceType string `json:"table_resource_type"`
}

// VPNAccessCatalog is the resolution boundary for the VPN-access signal. Only
// region resolution is needed (no sizing table), so RegionCatalog suffices.
type VPNAccessCatalog = RegionCatalog

// TranslateVPNAccess resolves a VPNAccessSpec into a concrete VPNAccessPlan using
// the catalog. Deterministic and catalog-driven: the csp_region comes from the
// region catalog (never invented). A non-AWS provider, or missing catalog data,
// surfaces as a hard plan-time error (never a silent fallback), per SPEC §4.
func TranslateVPNAccess(ctx context.Context, cat VPNAccessCatalog, spec VPNAccessSpec) (VPNAccessPlan, error) {
	if err := validateVPNAccessSpec(spec); err != nil {
		return VPNAccessPlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return VPNAccessPlan{}, err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))

	// The JIT door (WireGuard + a Keycloak EC2 SPI that mutates an AWS SG + a
	// DynamoDB allowlist) is an AWS-native pattern with no equivalent managed
	// primitive on other providers — reject them cleanly, never invent (SPEC §1/§4).
	if provider != ProviderAWS {
		return VPNAccessPlan{}, ErrComponentUnsupported{
			Component:   TypeVPNAccess,
			Provider:    spec.Provider,
			CSP:         row.CSP,
			CSPRegion:   row.CSPRegion,
			Alternative: "the JIT VPN door (WireGuard + Keycloak SPI-driven security-group ingress + DynamoDB allowlist) is AWS-only; deploy the corporate VPN place on aws",
		}
	}

	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = "pyxcloud-vpn"
	}
	port := spec.WireGuardPort
	if port == 0 {
		port = defaultWireGuardPort
	}
	table := strings.TrimSpace(spec.AllowlistTable)
	if table == "" {
		table = defaultJITAllowlistTable
	}
	pitr := true
	if spec.PointInTimeRecovery != nil {
		pitr = *spec.PointInTimeRecovery
	}

	plan := VPNAccessPlan{
		Provider:          provider,
		CSP:               row.CSP,
		RegionName:        row.RegionName,
		CSPRegion:         row.CSPRegion,
		LogicalName:       name,
		VPC:               strings.TrimSpace(spec.VPC),
		KeycloakRole:      strings.TrimSpace(spec.KeycloakRole),
		WireGuardPort:     port,
		BreakGlassCIDRs:   append([]string(nil), spec.BreakGlassCIDRs...),
		AllowlistTable:    table,
		PITR:              pitr,
		SGResourceType:    "aws_security_group",
		TableResourceType: "aws_dynamodb_table",
	}
	return plan, nil
}

func validateVPNAccessSpec(spec VPNAccessSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("vpn-access: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("vpn-access: provider is required (aws)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("vpn-access: unknown provider %q (aws)", spec.Provider)
	}
	if strings.TrimSpace(spec.KeycloakRole) == "" {
		return fmt.Errorf("vpn-access: keycloak_role (the IAM role name of the Keycloak instance running the JIT SPI) is required — the door is inert without a writer")
	}
	if spec.WireGuardPort != 0 && (spec.WireGuardPort < 1 || spec.WireGuardPort > 65535) {
		return fmt.Errorf("vpn-access: wireguard_port %d out of range (1-65535)", spec.WireGuardPort)
	}
	for _, c := range spec.BreakGlassCIDRs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			return fmt.Errorf("vpn-access: invalid break_glass_cidr %q: %w", c, err)
		}
	}
	return nil
}

// CanonicalVPNAccessType maps an accepted type token (vpn-access / jit-access /
// vpn-door) to the canonical vpn-access token, reporting whether it is recognised.
func CanonicalVPNAccessType(t string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case TypeVPNAccess, TypeJITAccess, TypeVPNDoor:
		return TypeVPNAccess, true
	default:
		return "", false
	}
}
