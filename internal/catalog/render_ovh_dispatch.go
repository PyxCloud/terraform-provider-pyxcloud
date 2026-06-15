package catalog

import (
	"fmt"
)

// This file reconciles the wave-2 OVHcloud provider into the SAME main
// translate/render dispatch the other seven wave-2 providers use. The OVH PR
// (#15) deliberately wired OVH only through cmd/pyxnet-render with its own
// OVHCatalog + dedicated renderers (render_ovh.go). To make all eight providers
// reachable through the identical EmbeddedCatalog → Translate* → Render*HCL path
// the topology resource walks, we:
//
//  1. fold the OVH region/flavor snapshot into EmbeddedCatalog (loadOVHCatalog),
//     so the common ResolveRegion / ResolveSKU / ResolveDBClass resolve OVH rows;
//  2. add `case ProviderOVH:` arms to the render switches (render.go /
//     render_macro.go) that adapt the common plan to OVH's existing renderers.
//
// OVH's renderers themselves are reused verbatim (RenderOVHNetworkHCL,
// RenderOVHManagedDatabaseHCL, RenderOVHKubernetesHCL, RenderOVHObjectStorageHCL);
// the adapters below only rebuild the OVH-shaped plan from the common plan so no
// renderer signature changes. Components OVH has no clean ovh/ovh primitive for
// (security-group, virtual-machine, scale-group, load-balancer, cache, messaging,
// dns/cdn/waf edge, secrets, serverless) intentionally have NO `case ProviderOVH:`
// arm and fall through to each switch's existing `default:` unsupported-provider
// error — the same clean plan-time error OVH's standalone path produced.

// loadOVHCatalog folds the embedded OVH region/flavor snapshot into the
// EmbeddedCatalog's shared indexes, mirroring loadAzure / foldLinodeCatalog /
// loadOracleCatalog. OVH regions become region rows (csp=ovh); OVH DB flavors
// become managed_database rows and node flavors become virtual_machine rows,
// replicated across every OVH region so the common ResolveSKU / ResolveDBClass
// (which key on csp_region) find them. A malformed snapshot is a hard error.
func (c *EmbeddedCatalog) loadOVHCatalog() error {
	oc, err := NewOVHCatalog()
	if err != nil {
		return fmt.Errorf("fold ovh catalog: %w", err)
	}
	for _, r := range oc.regions {
		c.rows = append(c.rows, r)
		k := key(r.CSP, r.RegionName)
		if _, exists := c.byCSPRegion[k]; !exists {
			c.byCSPRegion[k] = r
		}
		// DB flavors -> managed_database rows, one per (region, engine) pair OVH
		// supports, so a common ResolveDBClass(csp=ovh, csp_region, engine) matches.
		for _, f := range oc.dbFlavors {
			for _, eng := range []string{DBEnginePostgres, DBEngineMySQL} {
				c.mdbRows = append(c.mdbRows, MDBRow{
					Name:      f.Flavor,
					Family:    ovhFlavorFamily(f.Flavor),
					CSP:       cspOVH,
					CSPRegion: r.CSPRegion,
					Engine:    eng,
					CPU:       f.CPU,
					RAM:       f.RAM,
				})
				mk := mdbRegionEngineKey(cspOVH, r.CSPRegion, eng)
				c.mdbByRegionEng[mk] = append(c.mdbByRegionEng[mk], c.mdbRows[len(c.mdbRows)-1])
			}
		}
		// Node flavors -> virtual_machine rows (x86_64), so a common ResolveSKU
		// (used by managed-kubernetes node sizing) matches on (csp=ovh, region).
		for _, f := range oc.nodeFlavors {
			vm := VMRow{
				Name:              f.Flavor,
				Family:            ovhFlavorFamily(f.Flavor),
				CSP:               cspOVH,
				CSPRegion:         r.CSPRegion,
				Architecture:      ArchX8664,
				CPU:               f.CPU,
				RAM:               f.RAM,
				GPU:               "0",
				SupportsAutoscale: true,
			}
			c.vmRows = append(c.vmRows, vm)
			vk := vmRegionArchKey(cspOVH, r.CSPRegion, ArchX8664)
			c.vmByRegionArch[vk] = append(c.vmByRegionArch[vk], vm)
		}
	}
	return nil
}

// renderNetworkOVH adapts the common NetworkPlan to OVH's network renderer. OVH
// expresses a subnet as a CIDR + allocation range (start/end), derived here from
// the common plan's subnet CIDRs via ovhSubnetRange.
func renderNetworkOVH(p NetworkPlan) (string, error) {
	op := OVHNetworkPlan{
		RegionName: p.RegionName,
		CSPRegion:  p.CSPRegion,
		VPCName:    p.VPCName,
	}
	for _, s := range p.Subnets {
		start, end, err := ovhSubnetRange(s.CIDR)
		if err != nil {
			return "", fmt.Errorf("ovh network: %w", err)
		}
		op.Subnets = append(op.Subnets, OVHSubnetPlan{
			Name:    s.Name,
			Network: s.CIDR,
			Start:   start,
			End:     end,
		})
	}
	return RenderOVHNetworkHCL(op), nil
}

// renderManagedDatabaseOVH adapts the common ManagedDatabasePlan to OVH's DB
// renderer. The OVH cluster `plan` tier is not carried on the common plan, so it
// is re-resolved from the OVH flavor catalog by (cpu, ram).
func renderManagedDatabaseOVH(p ManagedDatabasePlan) (string, error) {
	oc, err := NewOVHCatalog()
	if err != nil {
		return "", err
	}
	flavor, err := oc.ResolveDBFlavor(p.CPU, p.RAM)
	if err != nil {
		return "", err
	}
	nodeCount := 1
	if p.HA {
		nodeCount = 3
	}
	op := OVHManagedDatabasePlan{
		RegionName:         p.RegionName,
		CSPRegion:          p.CSPRegion,
		DBName:             p.DBName,
		Engine:             p.Engine,
		OVHEngine:          ovhDBEngine(p.Engine),
		EngineVersion:      p.EngineVersion,
		Flavor:             flavor.Flavor,
		Plan:               flavor.Plan,
		CPU:                p.CPU,
		RAM:                p.RAM,
		DiskGB:             p.StorageGB,
		NodeCount:          nodeCount,
		HA:                 p.HA,
		DeletionProtection: p.DeletionProtection,
		NetworkName:        p.NetworkName,
	}
	return RenderOVHManagedDatabaseHCL(op), nil
}

// renderKubernetesOVH adapts the common K8sPlan to OVH's kube renderer. OVH kube
// runs in the regional variant of the macro region (e.g. GRA -> GRA11).
func renderKubernetesOVH(p K8sPlan) (string, error) {
	op := OVHK8sPlan{
		RegionName:   p.RegionName,
		CSPRegion:    ovhKubeRegion(p.CSPRegion),
		Name:         p.Name,
		Version:      p.Version,
		NodeFlavor:   p.NodeType,
		NodeCPU:      p.NodeCPU,
		NodeRAM:      p.NodeRAM,
		MinNodes:     p.MinNodes,
		MaxNodes:     p.MaxNodes,
		DesiredNodes: p.DesiredNodes,
		NetworkName:  p.NetworkName,
	}
	return RenderOVHKubernetesHCL(op), nil
}

// renderObjectStorageOVH adapts the common ObjectStoragePlan to OVH's storage
// renderer.
func renderObjectStorageOVH(p ObjectStoragePlan) (string, error) {
	op := OVHObjectStoragePlan{
		RegionName:  p.RegionName,
		CSPRegion:   p.CSPRegion,
		LogicalName: p.LogicalName,
		BucketName:  p.BucketName,
		Versioning:  p.Versioning,
		Public:      p.Public,
	}
	return RenderOVHObjectStorageHCL(op), nil
}
