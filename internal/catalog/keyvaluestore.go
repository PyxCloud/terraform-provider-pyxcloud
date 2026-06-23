package catalog

import (
	"context"
	"fmt"
	"strings"
)

// key-value-store is the abstract `key-value-store` component: a managed,
// schemaless key/value store (the canonical form of a DynamoDB table). Like
// object-storage and cache it has NO sizing catalog beyond the region — a managed
// KV store is region-scoped and billed per-usage / per-node, so the only catalog
// lookup is the region (region_name + provider -> csp_region). The component
// therefore depends on the RegionCatalog only, exactly like object-storage,
// cache and monitoring.
//
// Per-provider mapping:
//
//   - AWS: aws_dynamodb_table — the managed KV being migrated AWAY from. Rendered
//     PAY_PER_REQUEST (on-demand, no capacity planning), server-side encryption ON,
//     and point-in-time recovery ON (secure + durable by default).
//   - DigitalOcean: a Managed Redis cluster (digitalocean_database_cluster,
//     engine = "redis"). REDIS, NOT POSTGRES — justification: DynamoDB is a
//     schemaless, server-less managed key/value store; DO Managed Redis is the
//     closest managed KV that needs NO server to run and NO relational schema. DO
//     Managed PostgreSQL would force a relational schema/table model onto a
//     key/value workload, so Redis is the faithful KV translation. The node class
//     reuses the existing cache node ladder (cacheNodeClass) so a KV store and a
//     cache size identically.
//
// SECURITY INVARIANT: the KV store is PRIVATE/encrypted by default — DynamoDB
// server-side encryption is on; the DO Redis cluster is private to the place's
// VPC (no public connectivity), mirroring the cache component's posture.
//
// BACKEND BOUNDARY (FOLLOW-UP, NOT DONE HERE): the pyx-backend JIT-allowlist
// store (DdbStore) is the runtime consumer of this DynamoDB table. Rewiring
// DdbStore to a Redis/KV driver when an environment migrates AWS->DO is a SEPARATE
// backend task (it lives in the backend repo, not in this Terraform provider) and
// is intentionally NOT performed here. This component only translates the
// INFRASTRUCTURE; the BE store rewrite is tracked as the migration follow-up.

// Canonical key-value-store type tokens. `key-value-store` is canonical;
// `kv-store`, `keyvalue-store` and `dynamodb` are accepted aliases (all name the
// same component, mirroring the TopologyInspector vocabulary in SPEC §3.1).
const (
	TypeKeyValueStore = "key-value-store"
	TypeKVStore       = "kv-store"
	TypeKeyValue      = "keyvalue-store"
	TypeDynamoDB      = "dynamodb"
)

// KeyValueStoreSpec is the abstract description of a managed key/value store.
// Provider-neutral.
type KeyValueStoreSpec struct {
	Name     string // component name, e.g. "jit-allowlist"
	Region   string // abstract pyx region_name
	Provider string // aws | digitalocean | ...

	// PartitionKey is the primary key attribute name (DynamoDB). Empty -> "id".
	// On DO/Redis there is no schema, so this is informational only.
	PartitionKey string

	// MemoryGB is the requested store memory (DO Redis is sized by memory; the
	// node class is resolved from the shared cache ladder). 0 -> 1 GiB.
	MemoryGB int

	// HA enables a replica/standby. Defaults to single node.
	HA bool

	// Placement wiring (DO Redis is private to the place's VPC).
	Network string
}

// KeyValueStorePlan is the deterministic, catalog-resolved concrete translation
// of a KeyValueStoreSpec for one provider. STRUCTURED plan (not rendered .tf) —
// the provider owns rendering and state, consistent with the other components.
type KeyValueStorePlan struct {
	Provider   string `json:"provider"`    // aws | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)

	Name         string `json:"name"`
	PartitionKey string `json:"partition_key,omitempty"`

	// NodeClass / MemoryGB / HA / NetworkName apply to the DO Redis translation.
	NodeClass   string `json:"node_class,omitempty"`
	MemoryGB    int    `json:"memory_gb,omitempty"`
	HA          bool   `json:"ha"`
	Version     string `json:"version,omitempty"`
	NetworkName string `json:"network_name,omitempty"`

	ResourceType string `json:"resource_type"` // top provider resource
}

// TranslateKeyValueStore resolves a KeyValueStoreSpec into a concrete
// KeyValueStorePlan using the catalog. Deterministic and catalog-driven: the
// csp_region comes from the region catalog (never invented); the DO node class
// comes from the shared cache ladder. Any missing catalog data — or a provider
// with no managed KV primitive — surfaces as a hard plan-time error, per SPEC §4.
func TranslateKeyValueStore(ctx context.Context, cat RegionCatalog, spec KeyValueStoreSpec) (KeyValueStorePlan, error) {
	if err := validateKeyValueStoreSpec(spec); err != nil {
		return KeyValueStorePlan{}, err
	}
	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return KeyValueStorePlan{}, err
	}
	provider := lc(spec.Provider)
	name := canonicalName(spec.Name, "pyxcloud-kv")

	pk := strings.TrimSpace(spec.PartitionKey)
	if pk == "" {
		pk = "id"
	}

	plan := KeyValueStorePlan{
		Provider:     provider,
		CSP:          row.CSP,
		RegionName:   row.RegionName,
		CSPRegion:    row.CSPRegion,
		Name:         name,
		PartitionKey: pk,
		HA:           spec.HA,
	}

	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_dynamodb_table"
	case ProviderDigitalOcean:
		mem := spec.MemoryGB
		if mem <= 0 {
			mem = 1
		}
		plan.MemoryGB = mem
		plan.NodeClass = cacheNodeClass(ProviderDigitalOcean, mem) // shared cache ladder
		plan.Version = "7"                                         // DO managed Redis major line
		plan.NetworkName = spec.Network
		plan.ResourceType = "digitalocean_database_cluster"
	default:
		return KeyValueStorePlan{}, ErrComponentUnsupported{
			Component: TypeKeyValueStore, Provider: provider, CSP: row.CSP, CSPRegion: row.CSPRegion,
			Alternative: "key-value-store is supported on aws (aws_dynamodb_table) and digitalocean " +
				"(Managed Redis: digitalocean_database_cluster engine=redis); run a KV on a cache " +
				"component or self-host on a VM for other providers",
		}
	}
	return plan, nil
}

func validateKeyValueStoreSpec(spec KeyValueStoreSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("key-value-store: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("key-value-store: provider is required (aws | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("key-value-store: unknown provider %q (aws | digitalocean)", spec.Provider)
	}
	if spec.MemoryGB < 0 {
		return fmt.Errorf("key-value-store: memory_gb must be >= 0, got %d", spec.MemoryGB)
	}
	return nil
}

// CanonicalKeyValueStoreType maps an accepted type token to the canonical
// key-value-store token, reporting whether it is recognised.
func CanonicalKeyValueStoreType(t string) (string, bool) {
	switch lc(t) {
	case TypeKeyValueStore, TypeKVStore, TypeKeyValue, TypeDynamoDB:
		return TypeKeyValueStore, true
	default:
		return "", false
	}
}
