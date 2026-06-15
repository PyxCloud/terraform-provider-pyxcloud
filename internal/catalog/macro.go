package catalog

import (
	"fmt"
	"strings"
)

// This file carries the shared scaffolding for the remaining wave-1 macro
// components (pd-TF-REST-LAMBDA): cache, managed-queue, event-streaming,
// dns-zone, cdn-service, waf-service, managed-kubernetes, secrets-manager, and
// serverless-function. Each of those components follows the SAME abstract-first,
// catalog-driven pattern as the earlier components (region resolution -> concrete
// per-provider shaping -> structured plan -> render), and several of them are
// genuinely UNSUPPORTED on a provider that lacks the primitive. SPEC §1 forbids
// inventing a non-existent resource, so an unsupported provider surfaces as a
// clean plan-time error directing the user to the supported alternative — never a
// silent fallback or a fabricated resource.

// Canonical type tokens for the remaining macro components. Each constant is the
// canonical token; accepted aliases are mapped to it by the Canonical* helpers,
// mirroring the TopologyInspector vocabulary (SPEC §3.1).
const (
	TypeCache              = "cache"
	TypeManagedQueue       = "managed-queue"
	TypeMessageQueue       = "message-queue"
	TypeEventStreaming     = "event-streaming"
	TypeEventBus           = "event-bus"
	TypeDNSZone            = "dns-zone"
	TypeCDNService         = "cdn-service"
	TypeWAFService         = "waf-service"
	TypeManagedKubernetes  = "managed-kubernetes"
	TypeSecretsManager     = "secrets-manager"
	TypeServerlessFunction = "serverless-function"
)

// ErrComponentUnsupported is the canonical clean plan-time error for a macro
// component a provider has no native primitive for. It names the component, the
// provider/csp/region, and the supported alternative — never a silent fallback,
// never an invented resource (SPEC §1, §4).
type ErrComponentUnsupported struct {
	Component   string // canonical component type, e.g. "managed-queue"
	Provider    string // provider-facing name, e.g. "digitalocean"
	CSP         string // catalog csp token, e.g. "do"
	CSPRegion   string // resolved concrete region (when known)
	Alternative string // the supported alternative to use instead
}

func (e ErrComponentUnsupported) Error() string {
	region := e.CSPRegion
	if region == "" {
		region = "(unresolved)"
	}
	return fmt.Sprintf(
		"%s is not supported on provider %q (csp=%q, csp_region=%q): %s. "+
			"PyxCloud does not invent a non-existent resource — this is a hard plan-time "+
			"error, never a silent fallback.",
		e.Component, e.Provider, e.CSP, region, e.Alternative,
	)
}

// canonicalName derives a stable, provider-safe logical name from a user name,
// falling back to a default. It lower-cases and reduces to [a-z0-9-], collapsing
// invalid runs to a single hyphen and trimming the ends — the same DNS-ish shape
// the bucket/firewall names use, reused so every macro resource names identically.
func canonicalName(name, fallback string) string {
	out := sanitiseBucketPrefix(name)
	if out == "" {
		out = fallback
	}
	return out
}

// lc trims and lower-cases (used pervasively for provider/engine tokens).
func lc(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
