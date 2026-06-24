package catalog

import (
	"strconv"
	"strings"
)

// platform_asgs.go — pd-MIG-PORT-ASGS-CANONICAL (EPIC-AWS-TO-DO-MIGRATION).
//
// The PyxCloud platform services (SSO/Keycloak, the WireGuard VPN gateway, the
// observability aggregator, the SAST scanner, and the backend) were historically
// each a bespoke, per-cloud autoscaling group: hand-rolled AWS launch templates +
// aws_autoscaling_group per service, with no single abstract source of truth. That
// fork is exactly what the abstract-first provider exists to delete.
//
// This file expresses those 5 services in the CANONICAL vocabulary: each is a
// `virtual-machine-scale-group` of 1 (min=desired=1, self-healing) over the
// existing scale-group translator (scalegroup.go). On DigitalOcean a scale-group
// descends to a DOKS node-pool with auto_scale and min_nodes>=1 — which IS the
// self-healing "ASG-of-1" pattern (a failed node is replaced, capacity floor
// held). No new translator is introduced: the abstract topology is the single
// source and the renderer descends it, the whole point of the provider (SPEC §1).
//
// The droplet SIZE per service comes from the same `virtual_machine` SKU
// resolution every VM/scale-group uses (vm_catalog.csv) via the requested
// CPU/RAM — never a hand-picked instance type.

// PlatformService is one of the canonical PyxCloud control-plane services that
// migrates from a bespoke per-cloud ASG to a canonical scale-group.
type PlatformService struct {
	// Name is the canonical scale-group/component name (DNS-safe).
	Name string
	// CPU / RAM is the requested sizing, resolved to a concrete SKU by the catalog
	// (the SAME ResolveSKU path the virtual-machine component uses).
	CPU int
	RAM int
	// Health is the canonical health-check kind (elb = load-balancer health, which
	// also replaces unhealthy members — the production self-healing pattern).
	Health string
	// MinDesired is the self-heal floor: a scale-group of 1 keeps exactly one
	// healthy member and lets the platform replace a failed one. On DigitalOcean
	// this becomes a DOKS node-pool min_nodes=1 (DOKS forbids scale-to-zero).
	MinDesired int
	// Note documents which bespoke per-cloud ASG this replaces.
	Note string
}

// PlatformServices is the canonical mapping for the 5 platform-service ASGs. Each
// is a scale-group of 1 with load-balancer health (self-healing). Sizes are
// requested CPU/RAM; the catalog resolves them to a concrete provider SKU
// (droplet on DigitalOcean), so the same table is correct on every provider.
//
// Deterministic order (slice, not map) so the emitted topology and the round-trip
// plan are stable.
func PlatformServices() []PlatformService {
	return []PlatformService{
		{
			Name: "sso", CPU: 2, RAM: 4, Health: HealthELB, MinDesired: 1,
			Note: "Keycloak SSO — replaces the bespoke single-EC2/ASG; self-heal closes the random-502 outage class",
		},
		{
			Name: "vpn", CPU: 2, RAM: 2, Health: HealthEC2, MinDesired: 1,
			Note: "WireGuard VPN gateway — a UDP gateway, instance-liveness health (no LB in front); 2vCPU/2GiB resolves on both DO (s-2vcpu-2gb) and AWS (t3.small)",
		},
		{
			Name: "obs", CPU: 4, RAM: 8, Health: HealthELB, MinDesired: 1,
			Note: "observability aggregator — larger box for log/metric aggregation",
		},
		{
			Name: "sast", CPU: 2, RAM: 4, Health: HealthEC2, MinDesired: 1,
			Note: "SAST scanner (Semgrep/SonarQube) — batch worker, instance-liveness health",
		},
		{
			Name: "backend", CPU: 2, RAM: 4, Health: HealthELB, MinDesired: 1,
			Note: "pyx-backend (native) — fronted by the platform load-balancer, LB health-replace",
		},
	}
}

// PlatformScaleGroupComponents returns the 5 platform services as canonical
// AssembleComponent scale-groups, ready to drop into an AssembleInput. The
// architecture/OS default to the environment-wide defaults (x86_64/ubuntu) and
// the bootstrap user_data is threaded per service by the caller (e.g. the native
// binary pull + systemd unit); kubernetesVersion is forwarded so a DigitalOcean
// placement pins the DOKS control-plane version on the node-pool.
//
// arch/os/kubernetesVersion may be empty to take the canonical defaults.
//
// The platform-service BOOTSTRAP (the substance of each hand-written module — the
// Keycloak install for SSO, the WireGuard config for VPN, the native-binary pull
// for the backend, …) is threaded per service via PlatformBootstraps. A service
// with no entry there gets a bare scale-group (size + self-heal only). Slice 1
// (pd-DEP-MIGRATE-PLATFORM-MODULES) ports the SSO bootstrap; the other four
// follow the same pattern (see MIGRATION-PLATFORM-MODULES.md).
func PlatformScaleGroupComponents(arch, os, kubernetesVersion string) []AssembleComponent {
	return PlatformScaleGroupComponentsWithBootstrap(arch, os, kubernetesVersion, nil)
}

// PlatformBootstraps carries the per-service bootstrap user_data, keyed by the
// canonical service name ("sso" | "vpn" | "obs" | "sast" | "backend"). A missing
// key leaves that service's scale-group bare. Build the "sso" entry with
// RenderSSOBootstrapUserData.
type PlatformBootstraps map[string]string

// PlatformScaleGroupComponentsWithBootstrap is PlatformScaleGroupComponents plus
// the per-service bootstrap user_data. This is the wiring point that turns "a
// scale-group of 1" into "the canonical SSO/VPN/backend service": the bootstrap
// is baked into the scale-group launch template via AssembleScaleGroup.UserData,
// which the existing scale-group renderer descends to the provider's
// launch-template/cloud-init — no new translator (SPEC §1).
func PlatformScaleGroupComponentsWithBootstrap(arch, os, kubernetesVersion string, bootstraps PlatformBootstraps) []AssembleComponent {
	arch = strings.TrimSpace(arch)
	os = strings.TrimSpace(os)
	kubernetesVersion = strings.TrimSpace(kubernetesVersion)

	svcs := PlatformServices()
	out := make([]AssembleComponent, 0, len(svcs))
	for _, s := range svcs {
		out = append(out, AssembleComponent{
			Name: s.Name,
			Type: "virtual-machine-scale-group",
			ScaleGroup: &AssembleScaleGroup{
				Architecture: arch,
				CPU:          strconv.Itoa(s.CPU),
				RAM:          strconv.Itoa(s.RAM),
				OS:           os,
				// Scale-group of 1: min=desired=1 (self-heal floor), max=1 (a single
				// canonical platform member; scale the fleet by editing the abstract
				// topology, not by forking a per-cloud ASG).
				Min:               s.MinDesired,
				Max:               s.MinDesired,
				Desired:           s.MinDesired,
				Health:            s.Health,
				KubernetesVersion: kubernetesVersion,
				UserData:          bootstraps[s.Name],
			},
		})
	}
	return out
}
