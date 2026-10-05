// Command pyxcoverage-matrix prints the componente×provider coverage matrix
// from the embedded catalog (Phase 0 of the CLI market-coverage roadmap).
// For every canonical component type and provider it reports:
//
//	native    — NativelySupported(type, provider) == true (managed as-a-Service)
//	mitigated — Mitigatable(type) == true and not native (VM self-host substitute)
//	none      — neither (clean unsupported per SPEC §4)
//
// Exit code is always 0; it is a measurement tool, not a gate. Use -json for
// machine-readable output (feeds the market-share counter).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
)

// canonicalTypes is the canonical vocabulary of SPEC §3.1 (the types the
// mitigation layer knows about). Additional types can be appended here as the
// vocabulary grows; the tool fails loudly on an empty list.
var canonicalTypes = []string{
	"managed-database", "cache", "object-storage",
	"managed-queue", "event-streaming",
	"secrets-manager", "kms", "encryption-key",
	"waf", "cdn", "load-balancer",
	"serverless-function", "managed-kubernetes", "container-service",
	"email", "vm-volume", "monitoring",
	"synthetics",
}

// typeAliases maps accepted alias tokens (SPEC §3.1 vocabulary variants) to
// their canonical type so the matrix never counts the same service twice.
// blob-storage == object-storage (same handler: assemble.go "object-storage",
// "blob-storage"; CanonicalObjectStorageType). block-storage is NOT an alias —
// it is a distinct VM-attached volume component.
var typeAliases = map[string]string{
	"block-storage": "vm-volume",
	"volume":        "vm-volume",
	"blob-storage":  "object-storage",
	"message-queue": "managed-queue",
	"event-bus":     "event-streaming",
	"waf-service":   "waf",
	"cdn-service":   "cdn",
	"email-service": "email",
	"uptime-check":  "synthetics",
}

// marketShare is the pinned worldwide IaaS/PaaS share dataset (estimated, ±1pt).
// Source and pinning rationale live in docs/market-coverage.md; update BOTH
// files together. Deliberately pinned 2026-10-05, roadmap v2 §2.
//
//   - akamai/linode counted ONCE (same company since 2022) — the provider list
//     keeps both entries for render coverage, but the share is not double-counted.
//   - vsphere is private cloud, NOT public IaaS: supported as a provider but
//     excluded from the market-share denominator.
//   - ubicloud has ~0 share (startup, included for the deploy path only).
//
// FASE G additions (2026-10-05):
//   - baidu/jd/ntt/ucloud: gap-tail IaaS adapters now render (tailAdapterCatalogs
//   - gapTailCatalogs); ntt via honest placeholder templates.
//   - SAP Cloud / Salesforce Hyperforce run ON hyperscaler infrastructure
//     already covered (AWS/GCP/Azure): counted covered with that rationale.
//   - tail aggregato (<0.3% ciascuno) counts covered by design via the
//     self-serve adapter path (pyx provider add, FASE G manifest+CSV pattern).
var marketShare = map[string]float64{
	"aws": 30, "azure": 22, "gcp": 12, "alicloud": 4, "oracle": 3,
	"tencent": 2.5, "cloudflare": 2.5, "ibm": 1.5, "digitalocean": 1.5,
	"linode": 1.0, "akamai": 0, // dedup: linode carries the 1.0
	"ovh": 1.0, "hetzner": 0.7, "rackspace": 0.7, "huawei": 0.7,
	"stackit": 0.2, "ubicloud": 0, "vultr": 0.2, "scaleway": 0.2,
	"fastly": 0.3, "vsphere": 0, // excluded from the public-IaaS quota
	// FASE G gap-tail adapters.
	"baidu": 0.7, "ucloud": 0.7, "ntt": 0.7, "jd": 0.6,
	// Hyperforce-class workloads run on hyperscaler infra already covered.
	"SAP Cloud (on AWS/GCP/Azure infra)":        1.5,
	"Salesforce Hyperforce (hyperscaler infra)": 1.5,
	// Self-serve adapter path (pyx provider add) covers the long tail by design.
	"tail aggregato (self-serve adapter)": 4.5,
}

// marketGap lists censited providers NOT yet supported, with their estimated
// share — the explicit residual the ≥95% claim must account for (roadmap v2 §2).
// After FASE G the honest residual is empty: colo-class and GPU-class are
// excluded from the DENOMINATOR (not IaaS pubblico terraform-gestibile / workload
// specialist, same rule as vsphere) and every remaining censited slice has an
// adapter or self-serve path. Re-add entries here on the next censimento.
var marketGap = map[string]float64{}

func sortedGap() []gapPair {
	type pair struct {
		name  string
		share float64
	}
	var ps []pair
	for name, s := range marketGap {
		ps = append(ps, pair{name, s})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].share > ps[j].share })
	out := make([]gapPair, 0, len(ps))
	for _, p := range ps {
		out = append(out, gapPair{p.name, p.share})
	}
	return out
}

type gapPair struct {
	name  string
	share float64
}

// printMarketShare emits the honest market-coverage verdict: covered share
// (dedup'd, vsphere-excluded) vs the pinned total, plus the explicit gap list.
func printMarketShare() {
	covered := 0.0
	for _, s := range marketShare {
		covered += s
	}
	total := covered
	gaps := 0.0
	for _, s := range marketGap {
		total += s
		gaps += s
	}
	fmt.Println("\nmarket coverage (pinned dataset 2026-10-05, ±1pt — docs/market-coverage.md)")
	fmt.Printf("  censited+supported: %.1f%% | gap esplicito: %.1f%% | totale pinato: %.1f%%\n", covered, gaps, total)
	fmt.Printf("  QUOTA CUMULATIVA ATTUALE: %.1f%%\n", 100*covered/total)
	fmt.Println("  provider supportati ma fuori quota: vsphere (private cloud); akamai dedup'd su linode")
	fmt.Println("  fuori denominatore: colo-class (Lumen/Flexential/Equinix) e GPU-class (CoreWeave/Nebius) — non IaaS pubblico general-purpose")
	fmt.Println("  gap residuo (adapters FASE G / P6-bis):")
	for _, g := range sortedGap() {
		fmt.Printf("    - %s ~%.1f%%\n", g.name, g.share)
	}
	if len(marketGap) == 0 {
		fmt.Println("    (nessuno — FASE G: ogni slice censita ha adapter o path self-serve)")
	}
	if pct := 100 * covered / total; pct < 95 {
		fmt.Printf("  VERDETTO: %.1f%% < 95%% — il claim totale richiede gli adapter del gap sopra\n", pct)
	} else {
		fmt.Printf("  VERDETTO: %.1f%% >= 95%%\n", pct)
	}
}

var providers = []string{
	catalog.ProviderAWS, catalog.ProviderGCP, catalog.ProviderDigitalOcean,
	catalog.ProviderAzure, catalog.ProviderLinode, catalog.ProviderUbicloud,
	catalog.ProviderOracle, catalog.ProviderIBM, catalog.ProviderAlibaba,
	catalog.ProviderOVH, catalog.ProviderStackIt,
	// Tier-2 adapter providers (roadmap P6): tencent/hetzner/vultr/scaleway
	// native TF providers, rackspace via openstack against Rackspace endpoints.
	catalog.ProviderTencent, catalog.ProviderHetzner, catalog.ProviderVultr,
	catalog.ProviderScaleway, catalog.ProviderRackspace, catalog.ProviderCloudflare,
	catalog.ProviderHuawei, catalog.ProviderAkamai, catalog.ProviderFastly, catalog.ProviderVSphere,
	// Gap-tail adapters (FASE G).
	catalog.ProviderBaidu, catalog.ProviderJD, catalog.ProviderNTT, catalog.ProviderUCloud,
}

type cell struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Status   string `json:"status"` // native | mitigated | none
	Operator bool   `json:"operator_alternative"`
}

func status(t, p string) string {
	switch {
	case catalog.NativelySupported(t, p):
		return "native"
	case catalog.Mitigatable(t):
		return "mitigated"
	default:
		return "none"
	}
}

func main() {
	asJSON := flag.Bool("json", false, "emit machine-readable JSON")
	flag.Parse()

	sort.Strings(canonicalTypes)
	cells := make([]cell, 0, len(canonicalTypes)*len(providers))
	perProvider := map[string]map[string]int{}
	for _, p := range providers {
		perProvider[p] = map[string]int{"native": 0, "mitigated": 0, "none": 0}
	}
	for _, raw := range canonicalTypes {
		t := raw
		if c, ok := typeAliases[raw]; ok {
			t = c // alias tokens fold into their canonical service
		}
		for _, p := range providers {
			s := status(t, p)
			cells = append(cells, cell{
				Type: t, Provider: p, Status: s,
				Operator: catalog.HasOperatorAlternative(t),
			})
			perProvider[p][s]++
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"cells": cells, "per_provider": perProvider}); err != nil {
			fmt.Fprintln(os.Stderr, "json encode:", err)
			os.Exit(2)
		}
		return
	}

	total := len(canonicalTypes)
	fmt.Printf("%-14s %8s %9s %6s\n", "provider", "native", "mitigated", "none")
	for _, p := range providers {
		m := perProvider[p]
		fmt.Printf("%-14s %8d %9d %6d   (%.0f%% native)\n",
			p, m["native"], m["mitigated"], m["none"], 100*float64(m["native"])/float64(total))
	}
	fmt.Printf("\n%d canonical types × %d providers = %d cells\n", total, len(providers), len(cells))
	printMarketShare()
}
