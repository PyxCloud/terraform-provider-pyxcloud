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
	"managed-kubernetes":  {Image: "rancher/k3s:v1.30.6-k3s1", Port: 6443, CPU: 2, RAM: 8, envNote: "k3s (managed-kubernetes substitute)", degraded: true},
	"container-service":   {Image: "rancher/k3s:v1.30.6-k3s1", Port: 6443, CPU: 2, RAM: 8, envNote: "k3s (container-service substitute)", degraded: true},
	"load-balancer":       {Image: "haproxy:2.9", Port: 80, CPU: 1, RAM: 2, envNote: "HAProxy (load-balancer substitute)", degraded: true},
	"cdn-service":         {Image: "varnish:7", Port: 80, CPU: 2, RAM: 4, envNote: "Varnish HTTP cache (cdn-service)", degraded: true},
	"cdn":                 {Image: "varnish:7", Port: 80, CPU: 2, RAM: 4, envNote: "Varnish HTTP cache (cdn)", degraded: true},
	"email-service":       {Image: "bytemark/smtp:latest", Port: 25, CPU: 1, RAM: 2, envNote: "SMTP relay (email-service substitute)", degraded: true},
	"email":               {Image: "bytemark/smtp:latest", Port: 25, CPU: 1, RAM: 2, envNote: "SMTP relay (email substitute)", degraded: true},
	"block-storage":       {Image: "itsthenetwork/nfs-server-alpine:latest", Port: 2049, CPU: 1, RAM: 2, envNote: "NFS server (block-storage substitute)", degraded: true},
}

// Mitigatable reports whether a component type has a VM-hosted substitute.
func Mitigatable(componentType string) bool {
	_, ok := mitigationRecipe(componentType)
	return ok
}

var vaultHAAliasTypes = map[string]bool{
	"secrets-manager": true,
	"kms":             true,
	"encryption-key":  true,
}

// VaultHAAliasable reports whether a raw component should be redirected to the
// DigitalOcean Vault-HA operator path before the VM mitigation fallback runs.
func VaultHAAliasable(componentType string, provider string) bool {
	return vaultHAAliasTypes[mitigationType(componentType)] && mitigationType(provider) == ProviderDigitalOcean
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
	// secrets-manager on DO is now handled by the vault-ha operator auto-alias
	// (pd-MIG-B4-SECRETS-VAULT-AUTOALIAS): the assembler routes the raw type to the
	// Vault-HA operator-pattern component on DOKS instead of the single-VM mitigation.
	// Mark DO as natively supported so the mitigation fallback is NOT taken.
	"secrets-manager": {
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderIBM: true, ProviderAlibaba: true, ProviderStackIt: true,
		ProviderDigitalOcean: true,
	},
	"managed-queue": {
		// B1 (pd-MIG-B1-QUEUE-STREAM-OPERATORS): DO now routes through the RabbitMQ
		// Cluster Operator on DOKS instead of the single-VM mitigation. Mark DO as
		// natively supported so the mitigation fallback in assemble.go is NOT taken.
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderAlibaba: true, ProviderDigitalOcean: true,
	},
	"message-queue": {
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderAlibaba: true, ProviderDigitalOcean: true,
	},
	"event-streaming": {
		// B1 (pd-MIG-B1-QUEUE-STREAM-OPERATORS): DO now routes through the Strimzi
		// Kafka Operator on DOKS instead of the single-VM Redpanda mitigation.
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderIBM: true, ProviderAlibaba: true, ProviderDigitalOcean: true,
	},
	"event-bus": {
		ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true,
		ProviderIBM: true, ProviderAlibaba: true, ProviderDigitalOcean: true,
	},
	// kms/encryption-key on DO are also handled by the vault-ha auto-alias
	// (pd-MIG-B4-SECRETS-VAULT-AUTOALIAS): Vault Transit replaces KMS via the
	// operator-pattern component, not a single-VM fallback.
	"kms":            {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
	"encryption-key": {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
	// monitoring is native on DO via the LGTM operator-pattern stack (kube-prometheus-stack
	// + Loki + Grafana + Alertmanager), not a self-hosted-VM mitigation (pd-MIG-LGTM-MONITORING).
	"monitoring":   {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
	"synthetics":   {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
	"uptime-check": {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
	// DigitalOcean and Linode have no managed WAF; they route through Cloudflare WAF
	// (cloudflare_ruleset) instead of the degraded single-VM ModSecurity mitigation
	// (pd-MIG-B2-WAF-CLOUDFLARE). Mark both as natively supported so the mitigation
	// fallback in assemble.go is NOT taken for these providers.
	"waf-service":         {ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true, ProviderDigitalOcean: true, ProviderLinode: true},
	"waf":                 {ProviderAWS: true, ProviderGCP: true, ProviderAzure: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true, ProviderDigitalOcean: true, ProviderLinode: true},
	"serverless-function": {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true},
	"managed-kubernetes":  {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true, ProviderLinode: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true, ProviderOVH: true, ProviderStackIt: true},
	"container-service":   {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true, ProviderLinode: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true, ProviderOVH: true, ProviderStackIt: true},
	"load-balancer": {
		ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true,
		ProviderLinode: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true, ProviderStackIt: true,
	},
	"cdn-service":   {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true, ProviderAlibaba: true},
	"cdn":           {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true, ProviderAlibaba: true},
	"email-service": {ProviderAWS: true},
	"email":         {ProviderAWS: true},
	"block-storage": {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
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
	if strings.EqualFold(provider, ProviderUbicloud) {
		if cpu < 2 {
			cpu = 2
		}
		if ram < 8 {
			ram = 8
		}
	}
	return cpu, ram
}

// HasOperatorAlternative reports whether the given component type has a
// Kubernetes operator-pattern alternative (i.e. it can be replaced by a
// self-hosted operator install on DOKS rather than a managed cloud service or a
// bare-VM mitigation). This is used by the architecture-mismatch detector to
// identify when an environment is missing the operator layer it should have.
//
// Operator-pattern components are those for which the catalog ships a native
// DOKS rendering path via the operator convention (CORE helm_release + EXTRA
// kubernetes_manifest CRs) — they are NOT self-hosted on a VM.
func HasOperatorAlternative(componentType string) bool {
	switch strings.ToLower(strings.TrimSpace(componentType)) {
	case "monitoring", "synthetics", "uptime-check",
		"tracing", "distributed-tracing", "tempo", "trace-collector", "otel-tracing",
		"tls-certificate", "certificate", "cert-manager", "managed-certificate",
		"vault-ha", "vault", "vault-cluster",
		"workload-identity", "instance-identity", "workload-id",
		// B1 (pd-MIG-B1-QUEUE-STREAM-OPERATORS): queue/stream operator-pattern replacements.
		"managed-queue", "message-queue",
		"event-streaming", "event-bus":
		return true
	}
	return false
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
