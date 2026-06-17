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
	Image   string // container image to run
	Port    int    // primary service port (opened in user_data docker run)
	CPU     int    // default VM sizing
	RAM     int
	envNote string // a short note rendered into the script header
}

// selfHostRecipes maps a canonical component type to its VM-hosted substitute.
// These are the cross-provider, open-source equivalents of the managed services.
var selfHostRecipes = map[string]selfHostRecipe{
	"cache":            {Image: "redis:7", Port: 6379, CPU: 2, RAM: 4, envNote: "Redis (cache)"},
	"managed-database": {Image: "postgres:16", Port: 5432, CPU: 2, RAM: 4, envNote: "PostgreSQL (managed-database)"},
	"managed-queue":    {Image: "rabbitmq:3", Port: 5672, CPU: 2, RAM: 4, envNote: "RabbitMQ (managed-queue)"},
	"message-queue":    {Image: "rabbitmq:3", Port: 5672, CPU: 2, RAM: 4, envNote: "RabbitMQ (message-queue)"},
	"secrets-manager":  {Image: "hashicorp/vault:latest", Port: 8200, CPU: 2, RAM: 4, envNote: "Vault (secrets-manager)"},
	"object-storage":   {Image: "minio/minio:latest", Port: 9000, CPU: 2, RAM: 4, envNote: "MinIO (S3-compatible object-storage)"},
	"blob-storage":     {Image: "minio/minio:latest", Port: 9000, CPU: 2, RAM: 4, envNote: "MinIO (S3-compatible blob-storage)"},
	"event-streaming":  {Image: "docker.redpanda.com/redpandadata/redpanda:latest", Port: 9092, CPU: 2, RAM: 4, envNote: "Redpanda (Kafka-compatible event-streaming)"},
	"event-bus":        {Image: "docker.redpanda.com/redpandadata/redpanda:latest", Port: 9092, CPU: 2, RAM: 4, envNote: "Redpanda (event-bus)"},
}

// Mitigatable reports whether a component type has a VM-hosted substitute.
func Mitigatable(componentType string) bool {
	_, ok := selfHostRecipes[componentType]
	return ok
}

// nativeSupport records which providers have a managed as-a-Service for each
// mitigatable component (derived from the catalog's per-provider render support).
// A provider absent here for a type means: mitigate (self-host on a VM).
var nativeSupport = map[string]map[string]bool{
	"managed-database": {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderAzure: true, ProviderOVH: true, ProviderOracle: true, ProviderIBM: true, ProviderAlibaba: true, ProviderLinode: true},
	"cache":            {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
	"object-storage":   {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true, ProviderOVH: true},
	"blob-storage":     {ProviderAWS: true, ProviderGCP: true, ProviderDigitalOcean: true},
	"secrets-manager":  {ProviderAWS: true, ProviderGCP: true},
	"managed-queue":    {ProviderAWS: true, ProviderGCP: true},
	"message-queue":    {ProviderAWS: true, ProviderGCP: true},
	"event-streaming":  {ProviderAWS: true, ProviderGCP: true},
	"event-bus":        {ProviderAWS: true, ProviderGCP: true},
}

// NativelySupported reports whether the provider offers a managed service for the
// component type (so the native catalog translation is used, not a mitigation).
// Non-mitigatable types are always "native" (the catalog handles their support).
func NativelySupported(componentType, provider string) bool {
	if !Mitigatable(componentType) {
		return true
	}
	return nativeSupport[componentType][strings.ToLower(provider)]
}

// mitigateComponent renders the VM-hosted substitute for an unsupported component.
func mitigateComponent(ctx context.Context, cat VMCatalog, provider, region string,
	comp AssembleComponent, network, subnet, sg string) ([]string, error) {
	recipe, ok := selfHostRecipes[comp.Type]
	if !ok {
		return nil, fmt.Errorf("component %q: no mitigation recipe for type %q", comp.Name, comp.Type)
	}
	vmSpec := VMSpec{
		Name: comp.Name, Region: region, Provider: provider,
		Architecture: ArchX8664, CPU: recipe.CPU, RAM: recipe.RAM, OS: OSUbuntu, Count: 1,
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
	header := fmt.Sprintf("# pyxcloud mitigation: %s has no managed %q — self-hosting %s on a VM\n",
		provider, comp.Type, recipe.envNote)
	return []string{header + hcl}, nil
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
