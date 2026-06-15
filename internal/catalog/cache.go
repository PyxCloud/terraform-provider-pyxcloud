package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Cache is the abstract `cache` component (SPEC §5.8): a managed in-memory
// key-value store (Redis). It maps cleanly across all three wave-1 providers:
//
//   - AWS: aws_elasticache_replication_group (redis), in a subnet group, encrypted
//     in transit + at rest, NOT publicly reachable (the secure default).
//   - GCP: google_redis_instance (Memorystore for Redis), private-service-access
//     on the place's network, no public IP.
//   - DigitalOcean: digitalocean_database_cluster with engine = "redis", private
//     to the place's VPC.
//
// SECURITY INVARIANT: a cache is PRIVATE by default — it is reachable only from
// inside the place's network/security-group, never from the public internet.
// Transit + at-rest encryption are on where the provider supports the toggle.
//
// Cache engine token (canonical). Redis is the cross-provider lowest common
// denominator; memcached is AWS-only and therefore an exotic product we skip.
const (
	CacheEngineRedis = "redis"
)

// CacheSpec is the abstract description of a cache. Provider-neutral. Sizing is
// expressed as the canonical node size hint (cpu/ram); we pick the provider's
// smallest cache node class that meets it from a deterministic ladder, because
// the censused catalog does not carry a dedicated cache-node price table in the
// wave-1 snapshot (the `cache` catalog table is a wave-2 ETL). When that table
// lands the resolution moves to ResolveCacheNode exactly like ResolveSKU — the
// spec shape does not change.
type CacheSpec struct {
	Name     string // component name, e.g. "sessions"
	Region   string // abstract pyx region_name
	Provider string // aws | gcp | digitalocean

	Engine   string // redis (default; only redis is cross-provider)
	Version  string // engine version, e.g. "7.0"; empty -> provider default
	MemoryGB int    // requested memory (GiB); resolved to a node class

	// HA enables a replica/standby (multi-AZ failover). Defaults to single node.
	HA bool

	// Placement wiring.
	Network       string // canonical network/place name (the VPC)
	Subnets       []string
	SecurityGroup string
}

// CachePlan is the deterministic, catalog-resolved concrete translation.
type CachePlan struct {
	Provider   string `json:"provider"`
	CSP        string `json:"csp"`
	RegionName string `json:"region_name"`
	CSPRegion  string `json:"csp_region"`

	Name      string `json:"name"`
	Engine    string `json:"engine"`
	Version   string `json:"version"`
	NodeClass string `json:"node_class"` // concrete provider node/tier (deterministic ladder)
	MemoryGB  int    `json:"memory_gb"`
	HA        bool   `json:"ha"`

	Zones         []string `json:"zones"`
	NetworkName   string   `json:"network_name"`
	SubnetNames   []string `json:"subnet_names"`
	SecurityGroup string   `json:"security_group"`
	ResourceType  string   `json:"resource_type"`
}

// TranslateCache resolves a CacheSpec into a concrete CachePlan. Catalog-driven
// for the region (csp_region resolved, never invented); the node class comes from
// a deterministic per-provider ladder keyed off the requested memory (documented
// here as the resolution contract until the wave-2 cache price table lands). All
// three providers support managed Redis, so there is no unsupported path here.
func TranslateCache(ctx context.Context, cat RegionCatalog, spec CacheSpec) (CachePlan, error) {
	if err := validateCacheSpec(spec); err != nil {
		return CachePlan{}, err
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return CachePlan{}, err
	}
	provider := lc(spec.Provider)

	// Linode has no managed Redis/Valkey cache resource in the linode provider — the
	// `linode_database_*` resources cover PostgreSQL/MySQL only, not an in-memory
	// cache. Clean plan-time error rather than an invented resource.
	if provider == ProviderLinode {
		return CachePlan{}, ErrComponentUnsupported{
			Component: TypeCache, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "Linode has no managed Redis/Valkey resource (linode_database_* covers " +
				"PostgreSQL/MySQL only); use a cache on AWS (ElastiCache) or GCP (Memorystore), " +
				"or run self-managed Redis/Valkey on a linode_instance",
		}
	}

	mem := spec.MemoryGB
	if mem <= 0 {
		mem = 1
	}
	name := canonicalName(spec.Name, "pyxcloud-cache")
	nSubnets := len(spec.Subnets)
	if nSubnets == 0 {
		nSubnets = 1
	}

	plan := CachePlan{
		Provider:      provider,
		CSP:           row.CSP,
		RegionName:    row.RegionName,
		CSPRegion:     row.CSPRegion,
		Name:          name,
		Engine:        CacheEngineRedis,
		Version:       strings.TrimSpace(spec.Version),
		MemoryGB:      mem,
		HA:            spec.HA,
		Zones:         deriveZones(provider, row.CSPRegion, nSubnets),
		NetworkName:   spec.Network,
		SubnetNames:   spec.Subnets,
		SecurityGroup: spec.SecurityGroup,
	}
	plan.NodeClass = cacheNodeClass(provider, mem)
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_elasticache_replication_group"
		if plan.Version == "" {
			plan.Version = "7.0"
		}
	case ProviderGCP:
		plan.ResourceType = "google_redis_instance"
		if plan.Version == "" {
			plan.Version = "REDIS_7_0"
		}
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_database_cluster"
		if plan.Version == "" {
			plan.Version = "7"
		}
	case ProviderAzure:
		plan.ResourceType = "azurerm_redis_cache"
		if plan.Version == "" {
			plan.Version = "6" // Azure Cache for Redis major line
		}
	case ProviderOracle:
		// OCI Cache with Redis: oci_redis_redis_cluster. It is sized directly by
		// node_memory_in_gbs (no node-class token), so NodeClass is intentionally
		// empty and the renderer uses MemoryGB.
		plan.ResourceType = "oci_redis_redis_cluster"
		if plan.Version == "" {
			plan.Version = "7.0"
		}
	}
	return plan, nil
}

// cacheNodeClass picks the smallest provider cache node/tier whose memory meets
// the request, from a deterministic ladder. This is intentionally simple and pure
// (no hard-coded region maps); it is the documented stopgap until the wave-2
// `cache` price table is censused, after which it becomes a catalog ResolveCacheNode.
func cacheNodeClass(provider string, memGB int) string {
	switch provider {
	case ProviderAWS:
		// ElastiCache node types (memory-optimised r7g ladder; cache.t4g for tiny).
		switch {
		case memGB <= 1:
			return "cache.t4g.micro"
		case memGB <= 3:
			return "cache.t4g.small"
		case memGB <= 6:
			return "cache.t4g.medium"
		case memGB <= 13:
			return "cache.r7g.large"
		default:
			return "cache.r7g.xlarge"
		}
	case ProviderGCP:
		// Memorystore is sized purely by memory_size_gb; the "class" is the tier.
		return "BASIC" // STANDARD_HA is selected via the HA flag at render time
	case ProviderDigitalOcean:
		switch {
		case memGB <= 1:
			return "db-s-1vcpu-1gb"
		case memGB <= 2:
			return "db-s-1vcpu-2gb"
		case memGB <= 4:
			return "db-s-2vcpu-4gb"
		default:
			return "db-s-4vcpu-8gb"
		}
	}
	return ""
}

func validateCacheSpec(spec CacheSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("cache: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("cache: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("cache: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if e := lc(spec.Engine); e != "" && e != CacheEngineRedis {
		return fmt.Errorf("cache: engine %q is not cross-provider (only redis is supported wave-1; "+
			"memcached is an AWS-only exotic product)", spec.Engine)
	}
	if spec.MemoryGB < 0 {
		return fmt.Errorf("cache: memory_gb must be >= 0, got %d", spec.MemoryGB)
	}
	return nil
}

// CanonicalCacheType reports whether t names the cache component.
func CanonicalCacheType(t string) (string, bool) {
	if lc(t) == TypeCache {
		return TypeCache, true
	}
	return "", false
}
