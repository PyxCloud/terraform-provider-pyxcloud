package catalog

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// Provider rule-count limits. Security-group/firewall rule counts are bounded
// per provider; exceeding them is a hard plan-time error (never a silent trim).
//
//   - AWS: a default-quota security group allows 60 inbound + 60 outbound rules.
//     We budget per direction to mirror the real AWS limit.
//   - GCP: a VPC firewall policy caps rules; the safe per-firewall budget is
//     conservative because each direction renders as its own google_compute_firewall.
//   - DigitalOcean: a firewall allows up to 50 inbound + 50 outbound rules.
const (
	awsRulesPerDirectionMax = 60
	gcpRulesPerFirewallMax  = 100
	doRulesPerDirectionMax  = 50
	// Azure NSGs default to 1000 rules per NSG (across both directions); we budget
	// conservatively below the hard cap to leave room for Azure's default rules.
	azureRulesPerNSGMax = 900
	// IBM VPC security groups support a generous rule budget; the default IBM VPC
	// quota allows many rules per security group. We budget conservatively per
	// direction to mirror the AWS-style guard (a breach is a hard plan-time error).
	ibmRulesPerDirectionMax = 100
	// Alibaba: an ECS security group allows up to 200 rules per direction
	// (basic security group); we enforce that cap as a hard plan-time error.
	alibabaRulesPerDirectionMax = 200
	// StackIt: each rule is its own stackit_security_group_rule resource. The
	// per-security-group rule quota is conservative; budget per direction.
	stackitRulesPerDirectionMax = 50
)

// Protocol tokens (canonical, provider-neutral).
const (
	ProtoTCP  = "tcp"
	ProtoUDP  = "udp"
	ProtoICMP = "icmp"
	ProtoAll  = "all" // any protocol (renders to "-1" on AWS, all rules on others)
)

// Direction tokens.
const (
	DirIngress = "ingress"
	DirEgress  = "egress"
)

// SecurityRule is one abstract, provider-neutral firewall rule. A rule is either
// CIDR-scoped (CIDRs set) or references another security-group by canonical name
// (SourceSG set) — never both. Ports use an inclusive [FromPort, ToPort] range;
// for a single port both are equal. ICMP/all rules may leave ports at 0.
type SecurityRule struct {
	Direction string   // ingress | egress
	Protocol  string   // tcp | udp | icmp | all
	FromPort  int      // inclusive low port (0 when not port-based)
	ToPort    int      // inclusive high port
	CIDRs     []string // source (ingress) / destination (egress) CIDRs
	SourceSG  string   // canonical name of a peer security-group (mutually exclusive with CIDRs)
}

// SecurityGroupSpec is the abstract description of a security-group attached to a
// place's network — the canonical `security-group` (expose + explicit rules).
type SecurityGroupSpec struct {
	Name        string // SG name, e.g. "production-web"
	Network     string // canonical network/place name it attaches to (the VPC)
	Region      string // abstract pyx region_name (for csp_region resolution)
	Provider    string // provider-facing name: aws | gcp | digitalocean
	Description string // human description; sanitised to ASCII on render (AWS rejects non-ASCII)
	// Expose is the canonical shorthand: each listed TCP port is opened ingress
	// from 0.0.0.0/0 (e.g. [80,443]). Expanded into explicit rules at translate.
	Expose []int
	// Rules are explicit ingress/egress rules layered on top of Expose.
	Rules []SecurityRule
}

// RulePlan is one concrete, resolved firewall rule in the translated plan.
type RulePlan struct {
	Direction string   `json:"direction"`           // ingress | egress
	Protocol  string   `json:"protocol"`            // canonical proto token
	FromPort  int      `json:"from_port"`           // inclusive
	ToPort    int      `json:"to_port"`             // inclusive
	CIDRs     []string `json:"cidrs,omitempty"`     // cidr-scoped rule
	SourceSG  string   `json:"source_sg,omitempty"` // peer-SG-scoped rule (canonical name)
}

// SecurityGroupPlan is the deterministic, catalog-resolved concrete translation
// of a SecurityGroupSpec for one provider. STRUCTURED plan (not rendered .tf) —
// the provider owns rendering and state, consistent with NetworkPlan (§8).
type SecurityGroupPlan struct {
	Provider     string     `json:"provider"`      // aws | gcp | digitalocean
	CSP          string     `json:"csp"`           // catalog token: aws | gcp | do
	RegionName   string     `json:"region_name"`   // abstract pyx region
	CSPRegion    string     `json:"csp_region"`    // concrete provider region (catalog-resolved)
	SGName       string     `json:"sg_name"`       // logical SG/firewall name
	NetworkName  string     `json:"network_name"`  // the VPC/network it attaches to
	Description  string     `json:"description"`   // ASCII-sanitised description
	Rules        []RulePlan `json:"rules"`         // concrete rules (expose expanded + explicit)
	ResourceType string     `json:"resource_type"` // top provider resource, e.g. aws_security_group
}

// asciiOnly strips any non-ASCII byte from a description. AWS's
// SecurityGroupDescription only accepts ASCII (a non-ASCII description triggers
// an InvalidParameterValue API error — this caused a real incident), so the
// guard runs for every provider to keep behaviour identical and deterministic.
func asciiOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		// Printable ASCII range plus space; drop everything else (incl. control
		// chars and any rune > 0x7e).
		if r >= 0x20 && r <= 0x7e {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// IsASCII reports whether s contains only printable ASCII (exposed for tests and
// as the assertion guard the renderers rely on).
func IsASCII(s string) bool {
	for _, r := range s {
		if r > 0x7e {
			return false
		}
	}
	return true
}

// TranslateSecurityGroup resolves a SecurityGroupSpec into a concrete
// SecurityGroupPlan using the catalog. Deterministic and catalog-driven: the
// csp_region comes from the catalog (the SG lives in a resolved csp_region/VPC),
// expose ports are expanded into explicit ingress rules, the description is
// ASCII-sanitised, and provider rule limits are enforced — any missing catalog
// data or limit breach surfaces as a hard error (never a silent fallback/trim).
func TranslateSecurityGroup(ctx context.Context, cat RegionCatalog, spec SecurityGroupSpec) (SecurityGroupPlan, error) {
	if err := validateSGSpec(spec); err != nil {
		return SecurityGroupPlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return SecurityGroupPlan{}, err
	}

	name := spec.Name
	if name == "" {
		name = "pyxcloud-sg"
	}

	rules := make([]RulePlan, 0, len(spec.Expose)+len(spec.Rules))
	// Expand the canonical `expose` shorthand: each port opened TCP ingress from
	// anywhere (IPv4 + IPv6), deterministically ordered.
	exposed := append([]int(nil), spec.Expose...)
	sort.Ints(exposed)
	for _, port := range exposed {
		rules = append(rules, RulePlan{
			Direction: DirIngress,
			Protocol:  ProtoTCP,
			FromPort:  port,
			ToPort:    port,
			CIDRs:     []string{"0.0.0.0/0", "::/0"},
		})
	}
	// Layer explicit rules on top, normalised.
	for _, r := range spec.Rules {
		rules = append(rules, RulePlan{
			Direction: strings.ToLower(strings.TrimSpace(r.Direction)),
			Protocol:  strings.ToLower(strings.TrimSpace(r.Protocol)),
			FromPort:  r.FromPort,
			ToPort:    r.ToPort,
			CIDRs:     append([]string(nil), r.CIDRs...),
			SourceSG:  strings.TrimSpace(r.SourceSG),
		})
	}

	if err := enforceProviderCapabilities(spec.Provider, rules); err != nil {
		return SecurityGroupPlan{}, err
	}
	if err := enforceRuleLimits(spec.Provider, rules); err != nil {
		return SecurityGroupPlan{}, err
	}

	plan := SecurityGroupPlan{
		Provider:    strings.ToLower(spec.Provider),
		CSP:         row.CSP,
		RegionName:  row.RegionName,
		CSPRegion:   row.CSPRegion,
		SGName:      name,
		NetworkName: spec.Network,
		Description: asciiOnly(spec.Description),
		Rules:       rules,
	}
	if plan.Description == "" {
		plan.Description = "Managed by PyxCloud"
	}

	switch plan.Provider {
	case ProviderAWS:
		plan.ResourceType = "aws_security_group"
	case ProviderGCP:
		plan.ResourceType = "google_compute_firewall"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_firewall"
	case ProviderAzure:
		plan.ResourceType = "azurerm_network_security_group"
	case ProviderLinode:
		plan.ResourceType = "linode_firewall"
	case ProviderUbicloud:
		plan.ResourceType = "ubicloud_firewall"
	case ProviderOracle:
		plan.ResourceType = "oci_core_network_security_group"
	case ProviderIBM:
		plan.ResourceType = "ibm_is_security_group"
	case ProviderAlibaba:
		plan.ResourceType = "alicloud_security_group"
	case ProviderStackIt:
		plan.ResourceType = "stackit_security_group"
	}
	return plan, nil
}

// enforceProviderCapabilities rejects rules a provider cannot express, as a
// hard plan-time error (never a silent fallback). DigitalOcean firewalls support
// only tcp/udp/icmp protocols — there is no "all" protocol — so an `all` rule on
// DO must be expressed explicitly per protocol/port instead.
func enforceProviderCapabilities(provider string, rules []RulePlan) error {
	p := strings.ToLower(provider)
	// DigitalOcean, Linode and StackIt firewalls/security-group rules support only
	// named protocols (tcp/udp/icmp) — there is no "all"/"any" protocol — so an
	// `all` rule must be expressed explicitly per protocol (a hard plan-time error,
	// never a silent fallback).
	if p != ProviderDigitalOcean && p != ProviderLinode && p != ProviderStackIt {
		return nil
	}
	provName := "DigitalOcean"
	switch p {
	case ProviderLinode:
		provName = "Linode"
	case ProviderStackIt:
		provName = "StackIt"
	}
	for _, r := range rules {
		if r.Protocol == ProtoAll {
			return fmt.Errorf(
				"security-group: %s firewalls do not support the %q protocol; "+
					"declare explicit tcp/udp/icmp rules instead (this is a hard plan-time "+
					"error, never a silent fallback)", provName, ProtoAll)
		}
	}
	return nil
}

// enforceRuleLimits checks per-provider rule-count caps. The breach is a hard
// plan-time error (SPEC §5.2 "<= provider rule limits"), never a silent trim.
func enforceRuleLimits(provider string, rules []RulePlan) error {
	var ingress, egress int
	for _, r := range rules {
		if r.Direction == DirEgress {
			egress++
		} else {
			ingress++
		}
	}
	switch strings.ToLower(provider) {
	case ProviderAWS:
		if ingress > awsRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d ingress rules exceed the AWS limit of %d per security group", ingress, awsRulesPerDirectionMax)
		}
		if egress > awsRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d egress rules exceed the AWS limit of %d per security group", egress, awsRulesPerDirectionMax)
		}
	case ProviderGCP:
		if len(rules) > gcpRulesPerFirewallMax {
			return fmt.Errorf("security-group: %d rules exceed the GCP firewall limit of %d", len(rules), gcpRulesPerFirewallMax)
		}
	case ProviderDigitalOcean:
		if ingress > doRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d inbound rules exceed the DigitalOcean firewall limit of %d", ingress, doRulesPerDirectionMax)
		}
		if egress > doRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d outbound rules exceed the DigitalOcean firewall limit of %d", egress, doRulesPerDirectionMax)
		}
	case ProviderAzure:
		if len(rules) > azureRulesPerNSGMax {
			return fmt.Errorf("security-group: %d rules exceed the Azure NSG limit of %d", len(rules), azureRulesPerNSGMax)
		}
	case ProviderLinode:
		if ingress > linodeRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d inbound rules exceed the Linode firewall limit of %d per direction", ingress, linodeRulesPerDirectionMax)
		}
		if egress > linodeRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d outbound rules exceed the Linode firewall limit of %d per direction", egress, linodeRulesPerDirectionMax)
		}
	case ProviderIBM:
		if ingress > ibmRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d inbound rules exceed the IBM VPC security-group limit of %d", ingress, ibmRulesPerDirectionMax)
		}
		if egress > ibmRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d outbound rules exceed the IBM VPC security-group limit of %d", egress, ibmRulesPerDirectionMax)
		}
	case ProviderAlibaba:
		if ingress > alibabaRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d ingress rules exceed the Alibaba Cloud security-group limit of %d per direction", ingress, alibabaRulesPerDirectionMax)
		}
		if egress > alibabaRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d egress rules exceed the Alibaba Cloud security-group limit of %d per direction", egress, alibabaRulesPerDirectionMax)
		}
	case ProviderStackIt:
		if ingress > stackitRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d ingress rules exceed the StackIt limit of %d per security group", ingress, stackitRulesPerDirectionMax)
		}
		if egress > stackitRulesPerDirectionMax {
			return fmt.Errorf("security-group: %d egress rules exceed the StackIt limit of %d per security group", egress, stackitRulesPerDirectionMax)
		}
	}
	return nil
}

func validateSGSpec(spec SecurityGroupSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("security-group: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("security-group: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("security-group: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if len(spec.Expose) == 0 && len(spec.Rules) == 0 {
		return fmt.Errorf("security-group: at least one expose port or explicit rule is required")
	}
	for _, p := range spec.Expose {
		if p < 1 || p > 65535 {
			return fmt.Errorf("security-group: expose port %d out of range (1-65535)", p)
		}
	}
	for i, r := range spec.Rules {
		if err := validateRule(i, r); err != nil {
			return err
		}
	}
	return nil
}

func validateRule(i int, r SecurityRule) error {
	dir := strings.ToLower(strings.TrimSpace(r.Direction))
	if dir != DirIngress && dir != DirEgress {
		return fmt.Errorf("security-group: rule %d has invalid direction %q (ingress | egress)", i, r.Direction)
	}
	proto := strings.ToLower(strings.TrimSpace(r.Protocol))
	switch proto {
	case ProtoTCP, ProtoUDP, ProtoICMP, ProtoAll:
	default:
		return fmt.Errorf("security-group: rule %d has invalid protocol %q (tcp | udp | icmp | all)", i, r.Protocol)
	}
	// Exactly one scope: CIDRs xor SourceSG.
	hasCIDR := len(r.CIDRs) > 0
	hasSG := strings.TrimSpace(r.SourceSG) != ""
	if hasCIDR && hasSG {
		return fmt.Errorf("security-group: rule %d sets both cidrs and source_sg (mutually exclusive)", i)
	}
	if !hasCIDR && !hasSG {
		return fmt.Errorf("security-group: rule %d must set either cidrs or source_sg", i)
	}
	for _, c := range r.CIDRs {
		if _, _, err := net.ParseCIDR(c); err != nil {
			return fmt.Errorf("security-group: rule %d has invalid cidr %q: %w", i, c, err)
		}
	}
	// Port-based protocols need a sane inclusive range; icmp/all may omit ports.
	if proto == ProtoTCP || proto == ProtoUDP {
		if r.FromPort < 0 || r.FromPort > 65535 || r.ToPort < 0 || r.ToPort > 65535 {
			return fmt.Errorf("security-group: rule %d port range %d-%d out of range (0-65535)", i, r.FromPort, r.ToPort)
		}
		if r.ToPort < r.FromPort {
			return fmt.Errorf("security-group: rule %d to_port %d < from_port %d", i, r.ToPort, r.FromPort)
		}
	}
	return nil
}

// portRangeString renders an inclusive [from,to] range the way a firewall
// expects (single port collapses to "N", a range to "N-M").
func portRangeString(from, to int) string {
	if from == to {
		return strconv.Itoa(from)
	}
	return strconv.Itoa(from) + "-" + strconv.Itoa(to)
}
