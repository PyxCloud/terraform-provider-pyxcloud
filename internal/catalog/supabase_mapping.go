package catalog

// supabase_mapping.go — pd-MIGRATE-SUPABASE-OPTOUT-CANONICAL-TF
//
// SupabaseServiceMap translates Supabase self-hosted docker-compose service names
// and images to PyxCloud canonical component types (SPEC §3.1).
//
// This is a read-only lookup table — it carries zero rendering logic.
// Callers (e.g. the wizard scope-forest, architecture_detect, or a future
// `pyxcloud topology import --from supabase` command) call ResolveSupabaseService
// to convert a Supabase stack description into a canonical AssembleInput.
//
// Reference: docs/supabase-canonical-mapping.md (pyx-backend repo)

import "strings"

// SupabaseServiceKind categorises how the Supabase service is handled during opt-out.
type SupabaseServiceKind string

const (
	// SupabaseKindAbsorbed means the service is absorbed by an existing PyxCloud
	// component (e.g. Keycloak replaces GoTrue; PyxCloud console replaces Studio).
	SupabaseKindAbsorbed SupabaseServiceKind = "absorbed"
	// SupabaseKindCanonical means the service maps to a canonical component type
	// that the TF provider renders concretely per provider.
	SupabaseKindCanonical SupabaseServiceKind = "canonical"
	// SupabaseKindEliminated means the service is dropped entirely because the
	// canonical PyxCloud approach does not need it (e.g. PostgREST is replaced
	// by explicit backend API endpoints — no 1:1 canonical component).
	SupabaseKindEliminated SupabaseServiceKind = "eliminated"
)

// SupabaseMappingEntry describes one Supabase service and its canonical counterpart.
type SupabaseMappingEntry struct {
	// SupabaseService is the docker-compose service name (e.g. "db", "auth").
	SupabaseService string
	// KnownImages lists the container images that identify this service.
	KnownImages []string
	// Kind indicates how this service is handled in the canonical topology.
	Kind SupabaseServiceKind
	// CanonicalType is the PyxCloud canonical component type (SPEC §3.1).
	// Empty for SupabaseKindEliminated / SupabaseKindAbsorbed.
	CanonicalType string
	// AbsorbedBy names the existing PyxCloud component that replaces this service.
	// Non-empty only for SupabaseKindAbsorbed.
	AbsorbedBy string
	// Note is a human-readable migration note.
	Note string
}

// supabaseServiceTable is the authoritative Supabase→canonical mapping table.
// Order matches the Supabase self-hosted docker-compose reference (2024-Q4).
var supabaseServiceTable = []SupabaseMappingEntry{
	{
		SupabaseService: "db",
		KnownImages:     []string{"postgres:", "supabase/postgres"},
		Kind:            SupabaseKindCanonical,
		CanonicalType:   TypeManagedDatabase,
		Note:            "pg_dump/restore to canonical managed-database; preserve uuid-ossp, pgcrypto, pg_net extensions",
	},
	{
		SupabaseService: "auth",
		KnownImages:     []string{"supabase/gotrue"},
		Kind:            SupabaseKindAbsorbed,
		AbsorbedBy:      "keycloak",
		Note:            "Keycloak (existing PyxCloud SSO) replaces GoTrue; import users via auth.users export; update clients to OIDC/PKCE",
	},
	{
		SupabaseService: "rest",
		KnownImages:     []string{"postgrest/postgrest"},
		Kind:            SupabaseKindEliminated,
		Note:            "Auto-generated REST eliminated; use explicit pyx-backend Quarkus endpoints instead",
	},
	{
		SupabaseService: "realtime",
		KnownImages:     []string{"supabase/realtime"},
		Kind:            SupabaseKindCanonical,
		CanonicalType:   TypeEventStreaming,
		Note:            "pg_notify bridge to canonical event-streaming; WebSocket relay as virtual-machine sidecar",
	},
	{
		SupabaseService: "storage",
		KnownImages:     []string{"supabase/storage-api"},
		Kind:            SupabaseKindCanonical,
		CanonicalType:   TypeObjectStorage,
		Note:            "rclone/aws-sync to canonical object-storage; update signed URL generation; no public-read bucket policy",
	},
	{
		SupabaseService: "imgproxy",
		KnownImages:     []string{"darthsim/imgproxy"},
		Kind:            SupabaseKindCanonical,
		CanonicalType:   TypeVirtualMachine,
		Note:            "Optional: run as small VM sidecar (internal only) or delegate transforms to Cloudflare CDN edge",
	},
	{
		SupabaseService: "meta",
		KnownImages:     []string{"supabase/postgres-meta"},
		Kind:            SupabaseKindEliminated,
		Note:            "postgres-meta (Studio backend) eliminated; PyxCloud console replaces Studio entirely",
	},
	{
		SupabaseService: "functions",
		KnownImages:     []string{"supabase/edge-runtime"},
		Kind:            SupabaseKindCanonical,
		CanonicalType:   TypeServerlessFunction,
		Note:            "Rewrite Deno/TypeScript edge functions as Go or Java handlers; deploy via canonical serverless-function",
	},
	{
		SupabaseService: "analytics",
		KnownImages:     []string{"supabase/logflare"},
		Kind:            SupabaseKindAbsorbed,
		AbsorbedBy:      "lgtm-observability-stack",
		Note:            "Logflare replaced by LGTM (Grafana+Loki+Tempo+Prometheus) in existing PyxCloud observability ASG",
	},
	{
		SupabaseService: "kong",
		KnownImages:     []string{"kong:"},
		Kind:            SupabaseKindCanonical,
		CanonicalType:   TypeLoadBalancer,
		Note:            "Kong API gateway → canonical load-balancer + waf-service; Keycloak JWT validation at LB replaces Kong auth plugin",
	},
	{
		SupabaseService: "studio",
		KnownImages:     []string{"supabase/studio"},
		Kind:            SupabaseKindAbsorbed,
		AbsorbedBy:      "pyxcloud-console",
		Note:            "Supabase Studio replaced by PyxCloud console-frontend (admin UI behind VPN + SSO)",
	},
	{
		SupabaseService: "vector",
		KnownImages:     []string{"timberio/vector"},
		Kind:            SupabaseKindAbsorbed,
		AbsorbedBy:      "lgtm-observability-stack",
		Note:            "Vector log aggregator absorbed into LGTM observability stack (Alloy/Vector sidecar)",
	},
	{
		SupabaseService: "redis",
		KnownImages:     []string{"redis:"},
		Kind:            SupabaseKindCanonical,
		CanonicalType:   TypeCache,
		Note:            "Redis → canonical cache; TLS + AUTH required; no public exposure",
	},
}

// Canonical type constants referenced in the mapping table above.
// TypeObjectStorage is declared in objectstorage.go.
// TypeEventStreaming, TypeCache, TypeServerlessFunction are declared in macro.go.
const (
	TypeManagedDatabase = "managed-database"
	TypeVirtualMachine  = "virtual-machine"
	TypeLoadBalancer    = "load-balancer"
)

// LookupSupabaseService returns the mapping entry for a Supabase service name or
// container image prefix. Returns (entry, true) on match; (zero, false) if the
// service is not in the table.
func LookupSupabaseService(serviceNameOrImage string) (SupabaseMappingEntry, bool) {
	s := strings.ToLower(serviceNameOrImage)
	for _, e := range supabaseServiceTable {
		if strings.EqualFold(e.SupabaseService, s) {
			return e, true
		}
		for _, img := range e.KnownImages {
			if strings.HasPrefix(s, strings.ToLower(img)) {
				return e, true
			}
		}
	}
	return SupabaseMappingEntry{}, false
}

// SupabaseCanonicalComponents returns only the entries that produce a canonical
// PyxCloud component (Kind == SupabaseKindCanonical), i.e. the entries that
// require a corresponding canonical component in the assembled topology.
func SupabaseCanonicalComponents() []SupabaseMappingEntry {
	var out []SupabaseMappingEntry
	for _, e := range supabaseServiceTable {
		if e.Kind == SupabaseKindCanonical {
			out = append(out, e)
		}
	}
	return out
}

// SupabaseAbsorbedComponents returns entries where an existing PyxCloud component
// absorbs the Supabase service with no new canonical component needed.
func SupabaseAbsorbedComponents() []SupabaseMappingEntry {
	var out []SupabaseMappingEntry
	for _, e := range supabaseServiceTable {
		if e.Kind == SupabaseKindAbsorbed {
			out = append(out, e)
		}
	}
	return out
}
