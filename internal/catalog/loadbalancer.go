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

	TargetKind string `json:"target_kind"` // scale-group | vm
	TargetName string `json:"target_name"` // canonical name of the fronted component

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
	case ProviderIBM:
		plan.ResourceType = "ibm_is_lb"
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
