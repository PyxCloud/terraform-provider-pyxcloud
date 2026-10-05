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
	"managed-database", "cache", "object-storage", "blob-storage",
	"managed-queue", "message-queue", "event-streaming", "event-bus",
	"secrets-manager", "kms", "encryption-key",
	"waf-service", "waf", "cdn-service", "cdn", "load-balancer",
	"serverless-function", "managed-kubernetes", "container-service",
	"email-service", "email", "block-storage", "monitoring",
	"synthetics", "uptime-check",
}

var providers = []string{
	catalog.ProviderAWS, catalog.ProviderGCP, catalog.ProviderDigitalOcean,
	catalog.ProviderAzure, catalog.ProviderLinode, catalog.ProviderUbicloud,
	catalog.ProviderOracle, catalog.ProviderIBM, catalog.ProviderAlibaba,
	catalog.ProviderOVH, catalog.ProviderStackIt,
	// Tier-2 adapter providers (roadmap P6): tencent/hetzner/vultr/scaleway
	// native TF providers, rackspace via openstack against Rackspace endpoints.
	catalog.ProviderTencent, catalog.ProviderHetzner, catalog.ProviderVultr,
	catalog.ProviderScaleway, catalog.ProviderRackspace,
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
	for _, t := range canonicalTypes {
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
}
