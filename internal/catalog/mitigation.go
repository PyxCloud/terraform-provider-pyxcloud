package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Mitigation: when a provider has no managed as-a-Service for a canonical
// component, PyxCloud substitutes a self-hosted equivalent on a VM (the "VM or
// VM+DB" technique). This keeps a topology deployable on EVERY provider — the
// abstract component is honoured even where the cloud lacks the managed service,
// instead of a hard `unsupported` error.
//
// A mitigation renders the SAME building blocks the native path uses: a
// `virtual-machine` (TranslateVM + RenderVMHCL) wired into the env's network/SG,
// with a cloud-init `user_data` that installs Docker and runs the service
// container. The component's logical name is preserved.

// selfHostRecipe describes how to self-host a managed service on a VM.
type selfHostRecipe struct {
	Image    string // container image to run
	Port     int    // primary service port (opened in user_data docker run)
	CPU      int    // default VM sizing
	RAM      int
	envNote  string // a short note rendered into the script/header
	degraded bool   // true when the substitute preserves only the common edge behavior
}

// selfHostRecipes maps a canonical component type to its VM-hosted substitute.
// These are the cross-provider, open-source equivalents of the managed services.
//
// Deliberately absent: cloud IAM/access-policy/prefix-list. Those are policy or
// provider-control-plane constructs, not portable service daemons; pretending to
// emulate them on a VM would weaken the security model instead of preserving the
// topology.
var selfHostRecipes = map[string]selfHostRecipe{
	"cache":               {Image: "redis:7", Port: 6379, CPU: 2, RAM: 4, envNote: "Redis (cache)"},
	"managed-database":    {Image: "postgres:16", Port: 5432, CPU: 2, RAM: 4, envNote: "PostgreSQL (managed-database)"},
	"managed-queue":       {Image: "rabbitmq:3", Port: 5672, CPU: 2, RAM: 4, envNote: "RabbitMQ (managed-queue)"},
	"message-queue":       {Image: "rabbitmq:3", Port: 5672, CPU: 2, RAM: 4, envNote: "RabbitMQ (message-queue)"},
	"secrets-manager":     {Image: "hashicorp/vault:latest", Port: 8200, CPU: 2, RAM: 4, envNote: "Vault (secrets-manager)"},
	"object-storage":      {Image: "minio/minio:latest", Port: 9000, CPU: 2, RAM: 4, envNote: "MinIO (S3-compatible object-storage)"},
	"blob-storage":        {Image: "minio/minio:latest", Port: 9000, CPU: 2, RAM: 4, envNote: "MinIO (S3-compatible blob-storage)"},
	"event-streaming":     {Image: "docker.redpanda.com/redpandadata/redpanda:latest", Port: 9092, CPU: 2, RAM: 4, envNote: "Redpanda (Kafka-compatible event-streaming)"},
	"event-bus":           {Image: "docker.redpanda.com/redpandadata/redpanda:latest", Port: 9092, CPU: 2, RAM: 4, envNote: "Redpanda (event-bus)"},
	"kms":                 {Image: "hashicorp/vault:latest", Port: 8200, CPU: 2, RAM: 4, envNote: "Vault Transit (kms-compatible key service)"},
	"encryption-key":      {Image: "hashicorp/vault:latest", Port: 8200, CPU: 2, RAM: 4, envNote: "Vault Transit (encryption-key substitute)"},
	"monitoring":          {Image: "prom/prometheus:latest", Port: 9090, CPU: 2, RAM: 4, envNote: "Prometheus (monitoring)"},
	"synthetics":          {Image: "prom/blackbox-exporter:latest", Port: 9115, CPU: 1, RAM: 2, envNote: "Blackbox Exporter (synthetics)"},
	"uptime-check":        {Image: "prom/blackbox-exporter:latest", Port: 9115, CPU: 1, RAM: 2, envNote: "Blackbox Exporter (uptime-check)"},
	"waf-service":         {Image: "owasp/modsecurity-crs:nginx", Port: 80, CPU: 2, RAM: 4, envNote: "OWASP CRS / NGINX (waf-service)", degraded: true},
	"waf":                 {Image: "owasp/modsecurity-crs:nginx", Port: 80, CPU: 2, RAM: 4, envNote: "OWASP CRS / NGINX (waf)", degraded: true},
	"serverless-function": {Image: "quay.io/nuclio/dashboard:stable-amd64", Port: 8070, CPU: 2, RAM: 4, envNote: "Nuclio container FaaS (serverless-function)", degraded: true},
	"cdn-service":         {Image: "varnish:7", Port: 80, CPU: 2, RAM: 4, envNote: "Varnish HTTP cache (cdn-service)", degraded: true},
	"cdn":                 {Image: "varnish:7", Port: 80, CPU: 2, RAM: 4, envNote: "Varnish HTTP cache (cdn)", degraded: true},
}

// Mitigatable reports whether a component type has a VM-hosted substitute.
func Mitigatable(componentType string) bool {
	_, ok := mitigationRecipe(componentType)
	return ok
}

// nativeSupport records which providers have a managed as-a-Service for each
// mitigatable component (derived from the catalog's per-provider render support).
// A provider absent here for a type means: mitigate (self-host on a VM).
var nativeSupport = map[string]map[string]bool{
	// managed-database is native across all launch providers; Ubicloud's native
	// renderer is PostgreSQL-only, so unsupported engines still surface the native
	// clean error instead of being silently replaced with a different database.
	"managed-database": {
		ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true,
		ProviderLinode: true, ProviderUbicloud: true, ProviderOracle: true, ProviderIBM: true,
		ProviderAlibaba: true, ProviderOVH: true, ProviderStackIt: true,
	},
	"cache": {
		ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true,
		ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true,
	},
	"object-storage": {
		ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true,
		ProviderLinode: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true,
		ProviderOVH: true, ProviderStackIt: true,
	},
	"blob-storage": {
		ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true,
		ProviderLinode: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true,
		ProviderOVH: true, ProviderStackIt: true,
	},
	"secrets-manager": {
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderIBM: true, ProviderAlibaba: true, ProviderStackIt: true,
	},
	"managed-queue": {
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderAlibaba: true,
	},
	"message-queue": {
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderAlibaba: true,
	},
	"event-streaming": {
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderIBM: true, ProviderAlibaba: true,
	},
	"event-bus": {
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderIBM: true, ProviderAlibaba: true,
	},
	"kms":                 {ProviderAWS: true, ProviderGCP: true},
	"encryption-key":      {ProviderAWS: true, ProviderGCP: true},
	"monitoring":          {ProviderAWS: true, ProviderGCP: true},
	"synthetics":          {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
	"uptime-check":        {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
	"waf-service":         {ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true},
	"waf":                 {ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true},
	"serverless-function": {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true},
	"cdn-service":         {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true, ProviderAlibaba: true},
	"cdn":                 {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true, ProviderAlibaba: true},
}

// NativelySupported reports whether the provider offers a managed service for the
// component type (so the native catalog translation is used, not a mitigation).
// Non-mitigatable types are always "native" (the catalog handles their support).
func NativelySupported(componentType, provider string) bool {
	if !Mitigatable(componentType) {
		return true
	}
	typ := mitigationType(componentType)
	return nativeSupport[typ][strings.ToLower(provider)]
}

// mitigateComponent renders the VM-hosted substitute for an unsupported component.
func mitigateComponent(ctx context.Context, cat VMCatalog, provider, region string,
	comp AssembleComponent, network, subnet, sg string) ([]string, error) {
	recipe, ok := mitigationRecipe(comp.Type)
	if !ok {
		return nil, fmt.Errorf("component %q: no mitigation recipe for type %q", comp.Name, comp.Type)
	}
	cpu, ram := mitigationVMSizing(recipe, provider)
	vmSpec := VMSpec{
		Name: comp.Name, Region: region, Provider: provider,
		Architecture: ArchX8664, CPU: cpu, RAM: ram, OS: OSUbuntu, Count: 1,
		Network: network, Subnet: subnet, SecurityGroup: sg,
	}
	plan, err := TranslateVM(ctx, cat, vmSpec)
	if err != nil {
		return nil, fmt.Errorf("mitigation %q (%s): %w", comp.Name, comp.Type, err)
	}
	plan.UserData = selfHostUserData(recipe)
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		return nil, fmt.Errorf("mitigation %q render: %w", comp.Name, err)
	}
	degraded := ""
	if recipe.degraded {
		degraded = " (degraded substitute)"
	}
	header := fmt.Sprintf("# pyxcloud mitigation: %s has no managed %q — self-hosting %s on a VM%s using container image %s\n",
		provider, comp.Type, recipe.envNote, degraded, recipe.Image)
	return []string{header + hcl}, nil
}

func mitigationType(componentType string) string {
	return strings.ToLower(strings.TrimSpace(componentType))
}

func mitigationRecipe(componentType string) (selfHostRecipe, bool) {
	recipe, ok := selfHostRecipes[mitigationType(componentType)]
	return recipe, ok
}

func mitigationVMSizing(recipe selfHostRecipe, provider string) (int, int) {
	cpu, ram := recipe.CPU, recipe.RAM
	if strings.EqualFold(provider, ProviderUbicloud) && ram < 8 {
		ram = 8
	}
	return cpu, ram
}

// selfHostUserData is the cloud-init that installs Docker and runs the service.
func selfHostUserData(r selfHostRecipe) string {
	return fmt.Sprintf(`#!/bin/bash
set -euo pipefail
# pyxcloud self-host: %s
export DEBIAN_FRONTEND=noninteractive
apt-get update -y
apt-get install -y docker.io
systemctl enable --now docker
docker run -d --restart=always -p %d:%d --name pyx-service %s
`, r.envNote, r.Port, r.Port, r.Image)
}
