package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Listener protocols (canonical, provider-neutral). A load-balancer listener
// terminates on one of these; the canonical set is the cross-provider subset
// that maps cleanly to an AWS ALB listener, a GCP forwarding rule, and a DO
// loadbalancer forwarding rule. TCP is layer-4, HTTP/HTTPS are layer-7.
const (
	LBProtoHTTP  = "http"
	LBProtoHTTPS = "https"
	LBProtoTCP   = "tcp"
)

// Target kinds (canonical, provider-neutral). A load-balancer fronts either a
// scale-group (the autoscaled fleet — wired into the ASG/MIG target/backend) or
// a fixed set of virtual-machines (wired by instance attachment / instance group).
const (
	LBTargetScaleGroup = "scale-group"
	LBTargetVM         = "vm"
)

// AWS ALB listener rules cap the number of condition values at 5 per rule (a
// hard AWS quota). A canonical listener that fans out to more than this many
// path/host condition values cannot be expressed as a single ALB rule, so it is
// a hard plan-time error rather than a silently-truncated rule set.
const awsListenerConditionValuesMax = 5

// LBListenerSpec is one abstract listener: a port + protocol the load-balancer
// accepts traffic on. Conditions are optional layer-7 routing match values
// (host/path) used to shape the AWS ALB listener rule; they are bounded by the
// AWS per-rule condition-value quota.
type LBListenerSpec struct {
	Port     int    // listener port, e.g. 80 / 443
	Protocol string // http | https | tcp
	// Conditions are optional layer-7 match values (paths/hosts). Used only by
	// AWS to build an aws_lb_listener rule; bounded by the ALB 5-value quota.
	Conditions []string
	// Rules are optional layer-7 routing rules (pd-MIG-LB-L7-ROUTING) — full
	// ALB listener-rule parity: per-rule path/host match, explicit priority, and
	// the admin-VPN source-IP gate. When present they render as aws_lb_listener_rule
	// resources on AWS; on DigitalOcean they map to a DOKS ingress (see
	// LBRoutingRule). Empty = a single default forward action (the legacy shape).
	Rules []LBRoutingRule
}

// LBRoutingRule is one abstract layer-7 routing rule on a listener — the
// provider-neutral form of an AWS ALB aws_lb_listener_rule. It matches on host
// and/or path, carries an explicit evaluation priority, and optionally gates the
// match to an admin/VPN source-IP allow-list (the admin-VPN gate the existing
// AWS topology enforces with a source_ip condition).
//
// At least one of HostHeaders / PathPatterns must be set. AdminVPNCIDRs, when
// non-empty, additionally restricts the rule to those CIDRs (the admin/VPN
// allow-list) — on AWS a source_ip condition, on DO a documented ingress
// whitelist annotation. The combined condition VALUE count is bounded by the AWS
// ALB 5-values-per-rule quota (validated as a hard plan-time error).
type LBRoutingRule struct {
	Priority     int      // ALB rule priority (1-50000); lower = evaluated first. Required, unique per listener.
	HostHeaders  []string // host_header match values (e.g. "admin.example.com"); optional
	PathPatterns []string // path_pattern match values (e.g. "/admin/*"); optional
	// AdminVPNCIDRs is the admin/VPN source-IP allow-list. Non-empty pins the rule
	// to a source_ip condition (AWS) / source-range whitelist (DO) — the admin-VPN
	// gate. Empty = no source restriction.
	AdminVPNCIDRs []string
	// TargetName overrides the listener's default forward target (the canonical
	// name of a sibling scale-group/vm). Empty = forward to the LB's default
	// target group (the same target the default action uses).
	TargetName string
}

// LBHealthCheckSpec is the abstract health check the load-balancer runs against
// its targets. Empty fields default to a sane TCP/HTTP check on the first
// listener port.
type LBHealthCheckSpec struct {
	Protocol           string // http | https | tcp; defaults to the first listener's protocol
	Port               int    // check port; defaults to the first listener port
	Path               string // http(s) check path; defaults to "/"
	IntervalSeconds    int    // check interval; defaults to 30
	HealthyThreshold   int    // consecutive successes to mark healthy; defaults to 3
	UnhealthyThreshold int    // consecutive failures to mark unhealthy; defaults to 3
}

// LoadBalancerSpec is the abstract description of a load-balancer — the canonical
// `load-balancer { listeners, target, health_check, stickiness }`, placed in the
// region's network across its subnets/zones and fronting a scale-group (or a
// fixed set of VMs). Provider-neutral.
type LoadBalancerSpec struct {
	Name     string // load-balancer/component name, e.g. "web-lb"
	Region   string // abstract pyx region_name, e.g. "Dublin"
	Provider string // provider-facing name: aws | gcp | digitalocean

	Listeners   []LBListenerSpec  // at least one listener
	HealthCheck LBHealthCheckSpec // health check against the targets
	// Stickiness enables session affinity (lb_cookie on AWS, generated-cookie on
	// GCP/DO). Empty/false = round-robin.
	Stickiness bool

	// Target wiring. TargetKind is scale-group (the autoscaled fleet) or vm (a
	// fixed set). TargetName is the canonical name of that sibling component.
	TargetKind string
	TargetName string
	// TargetTag selects the fronted fleet by provider tag (a DO load-balancer's
	// droplet_tag). Empty -> "pyxcloud" (every instance).
	TargetTag string

	// StableIP degenerates the load-balancer to a stable public ingress IP when the
	// intent is "give this single instance a fixed address" rather than "balance a
	// fleet". On DigitalOcean a single-droplet pool fronted by a paid
	// digitalocean_loadbalancer (~$12/mo) that never balances is pure waste; with
	// StableIP the DO descent emits a free digitalocean_reserved_ip bound to the
	// target droplet instead. Requires a single VM target (TargetKind=vm,
	// TargetName set); DigitalOcean-only (aws/gcp keep aws_eip/google_compute_address
	// via a reserved-ip component). Empty/false = a normal load-balancer.
	StableIP bool

	// Placement wiring (from the other components). Network is the canonical
	// VPC/place name; Subnets is the set of canonical subnet names the LB spreads
	// across (multi-AZ, internet-facing); SecurityGroup is the SG to attach (AWS).
	Network       string
	Subnets       []string
	SecurityGroup string
}

// LBListenerPlan is one resolved listener in the translated plan.
type LBListenerPlan struct {
	Port       int      `json:"port"`
	Protocol   string   `json:"protocol"`
	Conditions []string `json:"conditions,omitempty"`
	// Rules are the resolved layer-7 routing rules, sorted by ascending priority
	// for a deterministic plan/render (pd-MIG-LB-L7-ROUTING).
	Rules []LBRoutingRulePlan `json:"rules,omitempty"`
}

// LBRoutingRulePlan is one resolved layer-7 routing rule (the catalog-resolved
// form of LBRoutingRule). Provider-neutral; rendered to aws_lb_listener_rule on
// AWS and a DOKS ingress rule on DigitalOcean.
type LBRoutingRulePlan struct {
	Priority      int      `json:"priority"`
	HostHeaders   []string `json:"host_headers,omitempty"`
	PathPatterns  []string `json:"path_patterns,omitempty"`
	AdminVPNCIDRs []string `json:"admin_vpn_cidrs,omitempty"` // admin/VPN source-IP gate
	TargetName    string   `json:"target_name,omitempty"`     // override forward target ("" = default TG)
}

// LBHealthCheckPlan is the resolved, defaulted health check in the plan.
type LBHealthCheckPlan struct {
	Protocol           string `json:"protocol"`
	Port               int    `json:"port"`
	Path               string `json:"path,omitempty"`
	IntervalSeconds    int    `json:"interval_seconds"`
	HealthyThreshold   int    `json:"healthy_threshold"`
	UnhealthyThreshold int    `json:"unhealthy_threshold"`
}

// LoadBalancerPlan is the deterministic, catalog-resolved concrete translation of
// a LoadBalancerSpec for one provider. STRUCTURED plan (not rendered .tf) — the
// provider owns rendering and state, consistent with ScaleGroupPlan / VMPlan (§8).
//
// The catalog has no `load_balancer` table snapshot for wave-1, so per SPEC §5.5
// the LB shape is provider-standard (ALB / forwarding-rule+backend / DO LB); the
// catalog still drives region resolution and the multi-AZ zone derivation. A
// future load_balancer(_price) snapshot can fill SKU/tier without changing this
// contract.
type LoadBalancerPlan struct {
	Provider   string `json:"provider"`    // aws | gcp | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | gcp | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)
	LBName     string `json:"lb_name"`     // logical load-balancer/component name

	Listeners   []LBListenerPlan  `json:"listeners"`
	HealthCheck LBHealthCheckPlan `json:"health_check"`
	Stickiness  bool              `json:"stickiness"`

	TargetKind string `json:"target_kind"`          // scale-group | vm
	TargetName string `json:"target_name"`          // canonical name of the fronted component
	TargetTag  string `json:"target_tag,omitempty"` // fleet-selection tag (DO droplet_tag); "" -> "pyxcloud"

	// StableIP: the DO load-balancer degenerates to a digitalocean_reserved_ip bound
	// to the single target droplet (cost-correct stable-ingress descent). See
	// LoadBalancerSpec.StableIP. When true, ResourceType is digitalocean_reserved_ip.
	StableIP bool `json:"stable_ip,omitempty"`

	// Zones are the concrete AZs/zones the LB spreads across (multi-AZ), derived
	// from the region catalog. Empty for DigitalOcean (region-scoped).
	Zones []string `json:"zones"`

	NetworkName   string   `json:"network_name"`   // VPC/network it lives in
	SubnetNames   []string `json:"subnet_names"`   // subnets the LB spreads across (multi-AZ)
	SecurityGroup string   `json:"security_group"` // SG to attach (AWS)
	ResourceType  string   `json:"resource_type"`  // top provider resource, e.g. aws_lb
}

// TranslateLoadBalancer resolves a LoadBalancerSpec into a concrete
// LoadBalancerPlan using the catalog. Deterministic and catalog-driven: the
// csp_region comes from the region catalog and the multi-AZ zones are derived
// deterministically from the csp_region (the SAME derivation the network and
// scale-group components use). The LB shape itself is provider-standard (no
// load_balancer SKU table for wave-1). Any missing catalog data — or a listener
// that breaches the AWS ALB condition-value quota — surfaces as a hard plan-time
// error (never a silent fallback / truncation).
func TranslateLoadBalancer(ctx context.Context, cat RegionCatalog, spec LoadBalancerSpec) (LoadBalancerPlan, error) {
	if err := validateLoadBalancerSpec(spec); err != nil {
		return LoadBalancerPlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return LoadBalancerPlan{}, err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))

	name := spec.Name
	if name == "" {
		name = "pyxcloud-lb"
	}

	listeners := make([]LBListenerPlan, 0, len(spec.Listeners))
	for _, l := range spec.Listeners {
		listeners = append(listeners, LBListenerPlan{
			Port:       l.Port,
			Protocol:   canonicalLBProto(l.Protocol),
			Conditions: append([]string(nil), l.Conditions...),
			Rules:      resolveRoutingRules(l.Rules),
		})
	}
	// Deterministic listener order (ascending port) so the rendered .tf and the
	// plan are stable regardless of input order.
	sort.SliceStable(listeners, func(i, j int) bool { return listeners[i].Port < listeners[j].Port })

	hc := defaultHealthCheck(spec.HealthCheck, listeners[0])

	// Multi-AZ spread: derive concrete zones from the region catalog. The LB
	// spreads across as many zones as it has subnets (at least one).
	subnets := spec.Subnets
	nSubnets := len(subnets)
	if nSubnets == 0 {
		nSubnets = 1
	}
	zones := deriveZones(provider, row.CSPRegion, nSubnets)

	targetKind := canonicalTargetKind(spec.TargetKind)

	// StableIP degeneration: only valid as a cost-correct DO descent over a single
	// concrete droplet target. A reserved IP binds to exactly one droplet_id, so it
	// cannot front a scale-group/tagged fleet — enforce a single VM target, and keep
	// it DigitalOcean-only (never a silent fallback on other providers, SPEC §4).
	if spec.StableIP {
		if provider != ProviderDigitalOcean {
			return LoadBalancerPlan{}, fmt.Errorf(
				"load-balancer %q: stable_ip degeneration is DigitalOcean-only (a "+
					"digitalocean_reserved_ip); on aws/gcp attach a reserved-ip component to the "+
					"instance or use a full load-balancer", name)
		}
		if targetKind != LBTargetVM || strings.TrimSpace(spec.TargetName) == "" {
			return LoadBalancerPlan{}, fmt.Errorf(
				"load-balancer %q: stable_ip requires a single VM target (target_kind=vm, "+
					"target_name set) — a reserved IP binds to one droplet and cannot front a "+
					"scale-group/tagged fleet", name)
		}
	}

	plan := LoadBalancerPlan{
		Provider:      provider,
		CSP:           row.CSP,
		RegionName:    row.RegionName,
		CSPRegion:     row.CSPRegion,
		LBName:        name,
		Listeners:     listeners,
		HealthCheck:   hc,
		Stickiness:    spec.Stickiness,
		TargetKind:    targetKind,
		TargetName:    strings.TrimSpace(spec.TargetName),
		TargetTag:     strings.TrimSpace(spec.TargetTag),
		StableIP:      spec.StableIP,
		Zones:         zones,
		NetworkName:   spec.Network,
		SubnetNames:   subnets,
		SecurityGroup: spec.SecurityGroup,
	}

	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_lb"
	case ProviderGCP:
		plan.ResourceType = "google_compute_forwarding_rule"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_loadbalancer"
		if spec.StableIP {
			plan.ResourceType = "digitalocean_reserved_ip"
		}
	case ProviderAzure:
		plan.ResourceType = "azurerm_lb"
	case ProviderLinode:
		plan.ResourceType = "linode_nodebalancer"
	case ProviderOracle:
		plan.ResourceType = "oci_load_balancer_load_balancer"
	case ProviderIBM:
		plan.ResourceType = "ibm_is_lb"
	case ProviderAlibaba:
		plan.ResourceType = "alicloud_alb_load_balancer"
	case ProviderStackIt:
		plan.ResourceType = "stackit_loadbalancer"
	}
	return plan, nil
}

// canonicalLBProto maps accepted protocol aliases to the canonical token.
func canonicalLBProto(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case LBProtoHTTPS, "ssl":
		return LBProtoHTTPS
	case LBProtoTCP, "l4":
		return LBProtoTCP
	default:
		return LBProtoHTTP
	}
}

// resolveRoutingRules copies and deterministically orders the abstract routing
// rules into resolved plan rules, sorted by ascending priority so the rendered
// .tf and the plan are stable regardless of input order. Slice fields are copied
// (never aliased) so the plan is independent of the input spec.
func resolveRoutingRules(in []LBRoutingRule) []LBRoutingRulePlan {
	if len(in) == 0 {
		return nil
	}
	out := make([]LBRoutingRulePlan, 0, len(in))
	for _, r := range in {
		out = append(out, LBRoutingRulePlan{
			Priority:      r.Priority,
			HostHeaders:   append([]string(nil), r.HostHeaders...),
			PathPatterns:  append([]string(nil), r.PathPatterns...),
			AdminVPNCIDRs: append([]string(nil), r.AdminVPNCIDRs...),
			TargetName:    strings.TrimSpace(r.TargetName),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out
}

// canonicalTargetKind maps accepted target-kind aliases to the canonical token.
// Empty defaults to scale-group (the production-default LB target).
func canonicalTargetKind(k string) string {
	switch strings.ToLower(strings.TrimSpace(k)) {
	case LBTargetVM, "virtual-machine", "instance":
		return LBTargetVM
	default:
		return LBTargetScaleGroup
	}
}

// defaultHealthCheck fills the abstract health check with sane defaults from the
// first listener when fields are unset.
func defaultHealthCheck(hc LBHealthCheckSpec, first LBListenerPlan) LBHealthCheckPlan {
	proto := canonicalLBProto(hc.Protocol)
	if strings.TrimSpace(hc.Protocol) == "" {
		proto = first.Protocol
	}
	port := hc.Port
	if port <= 0 {
		port = first.Port
	}
	path := strings.TrimSpace(hc.Path)
	if path == "" && (proto == LBProtoHTTP || proto == LBProtoHTTPS) {
		path = "/"
	}
	interval := hc.IntervalSeconds
	if interval <= 0 {
		interval = 30
	}
	healthy := hc.HealthyThreshold
	if healthy <= 0 {
		healthy = 3
	}
	unhealthy := hc.UnhealthyThreshold
	if unhealthy <= 0 {
		unhealthy = 3
	}
	return LBHealthCheckPlan{
		Protocol:           proto,
		Port:               port,
		Path:               path,
		IntervalSeconds:    interval,
		HealthyThreshold:   healthy,
		UnhealthyThreshold: unhealthy,
	}
}

func validateLoadBalancerSpec(spec LoadBalancerSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("load-balancer: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("load-balancer: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("load-balancer: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if len(spec.Listeners) == 0 {
		return fmt.Errorf("load-balancer: at least one listener is required")
	}
	for i, l := range spec.Listeners {
		if l.Port < 1 || l.Port > 65535 {
			return fmt.Errorf("load-balancer: listener %d port %d out of range (1-65535)", i, l.Port)
		}
		if p := strings.ToLower(strings.TrimSpace(l.Protocol)); p != "" {
			switch p {
			case LBProtoHTTP, LBProtoHTTPS, LBProtoTCP, "ssl", "l4":
			default:
				return fmt.Errorf("load-balancer: listener %d has invalid protocol %q (http | https | tcp)", i, l.Protocol)
			}
		}
		// AWS ALB listener rules cap condition values at 5. Reject a breach as a
		// hard plan-time error (never a silently-truncated rule set). The check is
		// AWS-specific but enforced uniformly so a plan is provider-portable.
		if strings.ToLower(strings.TrimSpace(spec.Provider)) == ProviderAWS && len(l.Conditions) > awsListenerConditionValuesMax {
			return fmt.Errorf(
				"load-balancer: listener %d has %d condition values, exceeding the AWS ALB "+
					"limit of %d per listener rule (this is a hard plan-time error, never a "+
					"silent truncation)", i, len(l.Conditions), awsListenerConditionValuesMax)
		}
		if err := validateRoutingRules(spec.Provider, i, l.Rules); err != nil {
			return err
		}
	}
	if k := strings.ToLower(strings.TrimSpace(spec.TargetKind)); k != "" {
		switch k {
		case LBTargetScaleGroup, "asg", "scalegroup", LBTargetVM, "virtual-machine", "instance":
		default:
			return fmt.Errorf("load-balancer: invalid target kind %q (scale-group | vm)", spec.TargetKind)
		}
	}
	if spec.HealthCheck.Port != 0 && (spec.HealthCheck.Port < 1 || spec.HealthCheck.Port > 65535) {
		return fmt.Errorf("load-balancer: health_check port %d out of range (1-65535)", spec.HealthCheck.Port)
	}
	if hp := strings.ToLower(strings.TrimSpace(spec.HealthCheck.Protocol)); hp != "" {
		switch hp {
		case LBProtoHTTP, LBProtoHTTPS, LBProtoTCP, "ssl", "l4":
		default:
			return fmt.Errorf("load-balancer: health_check has invalid protocol %q (http | https | tcp)", spec.HealthCheck.Protocol)
		}
	}
	return nil
}

// ALB listener-rule priority must be 1..50000 (an AWS hard quota). Priority 0 /
// negative is rejected, and priorities must be unique within a listener (ALB
// rejects duplicate priorities on a listener).
const (
	awsListenerRulePriorityMin = 1
	awsListenerRulePriorityMax = 50000
)

// validateRoutingRules enforces the layer-7 routing-rule invariants for a
// listener (pd-MIG-LB-L7-ROUTING): every rule matches on at least one host/path,
// priorities are in-range and unique within the listener, and the combined
// condition-VALUE count (hosts + paths + admin-VPN source CIDRs) stays within the
// AWS ALB 5-values-per-rule quota — all hard plan-time errors, never silent
// truncation. The quota is enforced uniformly so the plan is provider-portable.
func validateRoutingRules(provider string, listenerIdx int, rules []LBRoutingRule) error {
	if len(rules) == 0 {
		return nil
	}
	seenPriority := map[int]bool{}
	for j, r := range rules {
		if len(r.HostHeaders) == 0 && len(r.PathPatterns) == 0 {
			return fmt.Errorf(
				"load-balancer: listener %d rule %d must match on at least one host_header or path_pattern "+
					"(a layer-7 routing rule with no condition is not expressible)", listenerIdx, j)
		}
		if r.Priority < awsListenerRulePriorityMin || r.Priority > awsListenerRulePriorityMax {
			return fmt.Errorf(
				"load-balancer: listener %d rule %d has priority %d out of range (%d-%d); ALB listener-rule "+
					"priority is required and must be unique per listener (hard plan-time error)",
				listenerIdx, j, r.Priority, awsListenerRulePriorityMin, awsListenerRulePriorityMax)
		}
		if seenPriority[r.Priority] {
			return fmt.Errorf(
				"load-balancer: listener %d has duplicate routing-rule priority %d; ALB rejects duplicate "+
					"priorities on a listener (hard plan-time error, never a silent reorder)", listenerIdx, r.Priority)
		}
		seenPriority[r.Priority] = true

		// The AWS ALB caps the TOTAL condition values per rule at 5. The admin-VPN
		// source-IP gate (source_ip) consumes condition values too, so it is counted
		// here — a rule that combines many hosts/paths AND a wide CIDR allow-list can
		// breach the quota and must fail at plan time, never be silently truncated.
		if strings.ToLower(strings.TrimSpace(provider)) == ProviderAWS {
			total := len(r.HostHeaders) + len(r.PathPatterns) + len(r.AdminVPNCIDRs)
			if total > awsListenerConditionValuesMax {
				return fmt.Errorf(
					"load-balancer: listener %d rule %d has %d total condition values "+
						"(%d host + %d path + %d admin-VPN CIDR), exceeding the AWS ALB limit of %d "+
						"per listener rule (hard plan-time error, never a silent truncation)",
					listenerIdx, j, total, len(r.HostHeaders), len(r.PathPatterns), len(r.AdminVPNCIDRs),
					awsListenerConditionValuesMax)
			}
		}
	}
	return nil
}
