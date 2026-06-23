package catalog

import (
	"sort"
)

// securitybaseline.go — pd-DEP-SECURITY-BASELINE (EPIC-DEPLOY-PROCESS).
//
// A security-config baseline applied as a DEPLOY DEFAULT: the de-facto secure
// defaults derived from the topology itself, so an environment is least-privilege
// out of the box instead of relying on every author to hand-write hardening.
//
// The baseline is DERIVED from the canonical topology (not a fixed blob): it looks
// at what the environment actually declares (exposed ports, whether VMs/compute
// exist, whether secrets exist) and emits only the defaults that are warranted.
// It is conservative and ADDITIVE — it never widens access, only narrows the
// open defaults and fills in production-safe settings:
//
//   - security-group / firewall: a least-privilege egress posture. The assembler's
//     default egress is allow-all (0.0.0.0/0, every port) — operationally easy but
//     not least-privilege. The baseline replaces that with the minimal egress a
//     real workload needs (DNS + HTTPS + NTP), so an instance can resolve names,
//     pull artifacts over TLS and sync time, but cannot exfiltrate over arbitrary
//     ports. Authors who genuinely need wider egress add explicit rules.
//   - secrets-manager: production-safe defaults — keep the provider recovery window
//     (never force-destroy) so a secret is recoverable after an accidental delete.
//
// This is the SECURE-DEFAULTS half of the deploy baseline; least-priv IAM/KMS/WAF
// derivation is layered on the same DeriveSecurityBaseline seam as those
// components grow (the function returns a struct so callers consume only what they
// need). Renders/wiring for SG + secrets are implemented and tested here.

// Least-privilege baseline egress ports (TCP unless noted). Chosen as the minimal
// set a self-managed Linux workload needs to be operable:
//   - 53 (DNS, tcp+udp): name resolution.
//   - 443 (HTTPS): artifact/registry pulls, API calls, package mirrors over TLS.
//   - 123 (NTP, udp): clock sync (cert validation and logging depend on it).
const (
	baselineEgressDNS   = 53
	baselineEgressHTTPS = 443
	baselineEgressNTP   = 123
)

// SecurityBaseline is the derived secure-default set for an environment. Each
// field is the baseline contribution for one surface; a caller (the assembler)
// applies the parts relevant to the components it is rendering. Empty fields mean
// "the topology warrants no baseline for that surface".
type SecurityBaseline struct {
	// EgressRules is the least-privilege egress posture for the environment
	// security-group/firewall, derived only when the environment places compute
	// (a VM or scale-group) AND exposes ingress (i.e. an SG is actually emitted).
	// Empty when no SG is emitted. These REPLACE the assembler's allow-all egress.
	EgressRules []SecurityRule

	// SecretsForceDestroy is the production-safe default for every secrets-manager
	// component: false keeps the provider's delete recovery window so an accidental
	// destroy is recoverable. Pointer so an explicit author value is never clobbered.
	SecretsForceDestroy *bool
}

// boolPtr is a tiny helper for the optional *bool default fields (shared across
// the catalog: the security baseline and the managed-database test fixtures).
func boolPtr(b bool) *bool { return &b }

// DeriveSecurityBaseline computes the secure-default baseline for an environment
// from its canonical topology. Deterministic and provider-aware: the egress rule
// shape respects what each provider's firewall can express (DigitalOcean/Linode/
// StackIt reject the "all" protocol, so per-protocol rules are emitted there — the
// same capability rule the SG translator enforces).
//
// Pure: no catalog/network calls (it only inspects the declared topology), so it
// is cheap to call and trivially unit-testable.
func DeriveSecurityBaseline(in AssembleInput) SecurityBaseline {
	var b SecurityBaseline

	// Secrets: always production-safe (recoverable) unless the author overrides it
	// per-component. Derived whenever the environment declares any secrets-manager.
	for _, c := range in.Components {
		if _, ok := CanonicalSecretsType(c.Type); ok {
			b.SecretsForceDestroy = boolPtr(false)
			break
		}
	}

	// Egress lock-down: only when the environment places compute AND opens ingress,
	// i.e. exactly the condition under which the assembler emits an environment SG
	// (see AssembleHCL step 2). Otherwise there is no SG to harden.
	hasCompute := false
	for _, c := range in.Components {
		switch c.Type {
		case "virtual-machine", "virtual-machine-scale-group":
			hasCompute = true
		}
		// Mitigated components self-host on a VM too.
		if Mitigatable(c.Type) && !NativelySupported(c.Type, in.Provider) {
			hasCompute = true
		}
	}
	exposesIngress := len(in.Expose) > 0 || len(in.IngressRules) > 0
	if hasCompute && exposesIngress {
		b.EgressRules = baselineEgress()
	}

	return b
}

// baselineEgress builds the least-privilege egress rule set. We emit explicit
// per-protocol/port rules (tcp/udp) rather than collapsing to an "all" rule so the
// posture is identical and auditable across providers, and valid even on
// DigitalOcean/Linode/StackIt (which reject an "all" rule —
// enforceProviderCapabilities). Provider-neutral; deterministically ordered.
func baselineEgress() []SecurityRule {
	v4v6 := []string{"0.0.0.0/0", "::/0"}
	rules := []SecurityRule{
		// DNS over UDP and TCP.
		{Direction: DirEgress, Protocol: ProtoUDP, FromPort: baselineEgressDNS, ToPort: baselineEgressDNS, CIDRs: v4v6},
		{Direction: DirEgress, Protocol: ProtoTCP, FromPort: baselineEgressDNS, ToPort: baselineEgressDNS, CIDRs: v4v6},
		// HTTPS for artifact/registry/API egress.
		{Direction: DirEgress, Protocol: ProtoTCP, FromPort: baselineEgressHTTPS, ToPort: baselineEgressHTTPS, CIDRs: v4v6},
		// NTP for clock sync.
		{Direction: DirEgress, Protocol: ProtoUDP, FromPort: baselineEgressNTP, ToPort: baselineEgressNTP, CIDRs: v4v6},
	}
	// Stable order: by protocol then port.
	sort.SliceStable(rules, func(i, j int) bool {
		if rules[i].Protocol != rules[j].Protocol {
			return rules[i].Protocol < rules[j].Protocol
		}
		return rules[i].FromPort < rules[j].FromPort
	})
	return rules
}
