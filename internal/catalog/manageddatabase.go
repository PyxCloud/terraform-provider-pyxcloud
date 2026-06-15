package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Database engines (canonical, provider-neutral). The cross-provider subset that
// maps cleanly to AWS RDS, GCP Cloud SQL, and a DigitalOcean managed cluster.
// These are the engines the `managed_database` catalog table carries SKUs for.
const (
	DBEnginePostgres = "postgres"
	DBEngineMySQL    = "mysql"
)

// defaultEngineVersions maps a canonical engine to the version resolved when the
// user does not pin one. These are conservative, broadly-available major lines
// (the same defaults the wizard surfaces). Pinning `version` overrides this.
var defaultEngineVersions = map[string]string{
	DBEnginePostgres: "16",
	DBEngineMySQL:    "8.0",
}

// MDBRow mirrors one row of the backend `managed_database` table (columns:
// name, family, csp, region, engine, cpu, ram). `Name` is the concrete provider
// DB instance class / size token (e.g. AWS `db.t3.micro`, GCP `db-custom-2-4096`,
// DO `db-s-2vcpu-4gb`).
type MDBRow struct {
	Name      string // concrete DB instance class / size token
	Family    string // class family, e.g. db.t3 / db-custom / db-s
	CSP       string // catalog csp token: aws | gcp | do
	CSPRegion string // concrete provider region, e.g. eu-central-1
	Engine    string // postgres | mysql
	CPU       int    // vCPU count
	RAM       int    // GiB
}

// ManagedDatabaseSpec is the abstract description of a managed-database — the
// canonical `managed-database { engine, version, size (cpu/ram), storage_gb, ha,
// encrypted }`, placed in the region's network/subnets and reachable from the app
// security-group. Provider-neutral.
type ManagedDatabaseSpec struct {
	Name     string // managed-database/component name, e.g. "app-db"
	Region   string // abstract pyx region_name, e.g. "Frankfurt"
	Provider string // provider-facing name: aws | gcp | digitalocean

	Engine    string // postgres | mysql
	Version   string // engine version; defaults per defaultEngineVersions
	CPU       int    // requested vCPU (resolved to a concrete DB class)
	RAM       int    // requested RAM (GiB)
	StorageGB int    // allocated storage in GiB
	HA        bool   // high availability (Multi-AZ / regional / standby node)
	Encrypted bool   // storage encryption at rest

	// DeletionProtection guards the DB against accidental destroy. It defaults to
	// true (production-intent) — the TEST round-trip override sets it false ONLY so
	// teardown is clean. Pointer so an unset value can take the production default.
	DeletionProtection *bool
	// SkipFinalSnapshot disables the final snapshot on destroy. It defaults to
	// false (production-intent: always take a final snapshot). The TEST round-trip
	// override sets it true ONLY so teardown is clean. Pointer so an unset value can
	// take the production default.
	SkipFinalSnapshot *bool

	// Placement wiring (from the other components). Network is the canonical
	// VPC/place name; Subnets is the set of canonical subnet names the DB subnet
	// group spreads across (multi-AZ); SecurityGroup is the app SG the DB is
	// reachable from (AWS).
	Network       string
	Subnets       []string
	SecurityGroup string
}

// ManagedDatabasePlan is the deterministic, catalog-resolved concrete translation
// of a ManagedDatabaseSpec for one provider. STRUCTURED plan (not rendered .tf) —
// the provider owns rendering and state, consistent with LoadBalancerPlan /
// ScaleGroupPlan / VMPlan (§8).
//
// The concrete DB instance class (DBClass) is resolved from the `managed_database`
// catalog table where present; missing catalog data is a hard plan-time error
// (never an invented class), per SPEC §4.
type ManagedDatabasePlan struct {
	Provider   string `json:"provider"`    // aws | gcp | digitalocean
	CSP        string `json:"csp"`         // catalog token: aws | gcp | do
	RegionName string `json:"region_name"` // abstract pyx region
	CSPRegion  string `json:"csp_region"`  // concrete provider region (catalog-resolved)
	DBName     string `json:"db_name"`     // logical managed-database/component name

	Engine        string `json:"engine"`         // postgres | mysql
	EngineVersion string `json:"engine_version"` // resolved engine version
	DBClass       string `json:"db_class"`       // concrete DB instance class/size (catalog-resolved)
	Family        string `json:"family"`         // class family
	CPU           int    `json:"cpu"`            // resolved vCPU
	RAM           int    `json:"ram"`            // resolved RAM (GiB)
	StorageGB     int    `json:"storage_gb"`     // allocated storage
	HA            bool   `json:"ha"`             // high availability
	Encrypted     bool   `json:"encrypted"`      // storage encryption at rest

	DeletionProtection bool `json:"deletion_protection"` // resolved (default true)
	SkipFinalSnapshot  bool `json:"skip_final_snapshot"` // resolved (default false)

	// Zones are the concrete AZs/zones the DB subnet group spreads across (multi-AZ
	// for HA), derived from the region catalog. Empty for DigitalOcean.
	Zones []string `json:"zones"`

	NetworkName   string   `json:"network_name"`   // VPC/network it lives in
	SubnetNames   []string `json:"subnet_names"`   // subnets the DB subnet group spreads across
	SecurityGroup string   `json:"security_group"` // app SG the DB is reachable from (AWS)
	ResourceType  string   `json:"resource_type"`  // top provider resource, e.g. aws_db_instance
}

// MinStorageGB is the floor for allocated DB storage. RDS rejects an
// allocated_storage below 20 GiB for the supported engines; we clamp the
// production default to this rather than reject a smaller intent. The TEST
// fixture uses exactly this minimum.
const MinStorageGB = 20

// MDBCatalog is the resolution boundary for managed-database DB classes. Both the
// embedded snapshot and a future live-BE client satisfy it, so the provider never
// embeds DB-class tables of its own.
type MDBCatalog interface {
	RegionCatalog
	// ResolveDBClass resolves {csp, csp_region, engine, cpu, ram} into the concrete
	// provider DB instance class from the `managed_database` catalog. It returns
	// ErrDBClassNotFound (listing nearest sizes) when nothing matches — never a
	// silent fallback to a different size.
	ResolveDBClass(ctx context.Context, csp, cspRegion, engine string, cpu, ram int) (MDBRow, error)
}

// ErrDBClassNotFound is returned when no managed_database row matches the request.
// It lists the nearest available sizes in that csp/region/engine to guide the user.
type ErrDBClassNotFound struct {
	CSP       string
	CSPRegion string
	Engine    string
	CPU       int
	RAM       int
	Nearest   []MDBRow
}

func (e ErrDBClassNotFound) Error() string {
	var sizes []string
	for _, r := range e.Nearest {
		sizes = append(sizes, fmt.Sprintf("%s (%dvCPU/%dGiB)", r.Name, r.CPU, r.RAM))
	}
	nearest := "none in this region/engine"
	if len(sizes) > 0 {
		nearest = strings.Join(sizes, ", ")
	}
	return fmt.Sprintf(
		"no managed_database class for csp=%q csp_region=%q engine=%q cpu=%d ram=%dGiB: "+
			"the PyxCloud managed_database catalog has no DB instance class matching that "+
			"sizing. Nearest available sizes: %s (this is a hard plan-time error, never a "+
			"silent fallback)",
		e.CSP, e.CSPRegion, e.Engine, e.CPU, e.RAM, nearest,
	)
}

// TranslateManagedDatabase resolves a ManagedDatabaseSpec into a concrete
// ManagedDatabasePlan using the catalog. Deterministic and catalog-driven: the
// csp_region comes from the region catalog, the DB instance class from the
// managed_database catalog, and the multi-AZ zones derived deterministically from
// the csp_region (the SAME derivation the network/scale-group/load-balancer
// components use). Production-intent defaults are applied here:
// deletion_protection=true and skip_final_snapshot=false (a final snapshot is
// taken) unless the spec explicitly overrides them (the test-only override). Any
// missing catalog data surfaces as a hard plan-time error (never a silent fallback).
func TranslateManagedDatabase(ctx context.Context, cat MDBCatalog, spec ManagedDatabaseSpec) (ManagedDatabasePlan, error) {
	if err := validateManagedDatabaseSpec(spec); err != nil {
		return ManagedDatabasePlan{}, err
	}

	row, err := cat.ResolveRegion(ctx, spec.Region, spec.Provider)
	if err != nil {
		return ManagedDatabasePlan{}, err
	}

	provider := strings.ToLower(strings.TrimSpace(spec.Provider))

	engine := canonicalEngine(spec.Engine)
	version := strings.TrimSpace(spec.Version)
	if version == "" {
		version = defaultEngineVersions[engine]
	}

	dbClass, err := cat.ResolveDBClass(ctx, row.CSP, row.CSPRegion, engine, spec.CPU, spec.RAM)
	if err != nil {
		return ManagedDatabasePlan{}, err
	}

	storage := spec.StorageGB
	if storage < MinStorageGB {
		storage = MinStorageGB
	}

	name := spec.Name
	if name == "" {
		name = "pyxcloud-db"
	}

	// Production-intent defaults: deletion_protection=true, a final snapshot taken
	// (skip_final_snapshot=false). The test-only override flips these via pointers.
	deletionProtection := true
	if spec.DeletionProtection != nil {
		deletionProtection = *spec.DeletionProtection
	}
	skipFinalSnapshot := false
	if spec.SkipFinalSnapshot != nil {
		skipFinalSnapshot = *spec.SkipFinalSnapshot
	}

	// Multi-AZ spread for the DB subnet group: derive concrete zones from the
	// region catalog. The DB spreads across as many zones as it has subnets (at
	// least two for HA; at least one otherwise).
	subnets := spec.Subnets
	nSubnets := len(subnets)
	if nSubnets == 0 {
		nSubnets = 1
	}
	zones := deriveZones(provider, row.CSPRegion, nSubnets)

	plan := ManagedDatabasePlan{
		Provider:           provider,
		CSP:                row.CSP,
		RegionName:         row.RegionName,
		CSPRegion:          row.CSPRegion,
		DBName:             name,
		Engine:             engine,
		EngineVersion:      version,
		DBClass:            dbClass.Name,
		Family:             dbClass.Family,
		CPU:                dbClass.CPU,
		RAM:                dbClass.RAM,
		StorageGB:          storage,
		HA:                 spec.HA,
		Encrypted:          spec.Encrypted,
		DeletionProtection: deletionProtection,
		SkipFinalSnapshot:  skipFinalSnapshot,
		Zones:              zones,
		NetworkName:        spec.Network,
		SubnetNames:        subnets,
		SecurityGroup:      spec.SecurityGroup,
	}

	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_db_instance"
	case ProviderGCP:
		plan.ResourceType = "google_sql_database_instance"
	case ProviderDigitalOcean:
		plan.ResourceType = "digitalocean_database_cluster"
	case ProviderAzure:
		if engine == DBEngineMySQL {
			plan.ResourceType = "azurerm_mysql_flexible_server"
		} else {
			plan.ResourceType = "azurerm_postgresql_flexible_server"
		}
	case ProviderLinode:
		plan.ResourceType = "linode_database_postgresql_v2"
	case ProviderUbicloud:
		// Ubicloud Managed Database is PostgreSQL-only (ubicloud_postgres). A MySQL
		// engine has no Ubicloud resource; the render step rejects it cleanly. We
		// still set the postgres resource type here so a postgres plan is concrete.
		plan.ResourceType = "ubicloud_postgres"
	case ProviderOracle:
		// OCI's managed-database split: PostgreSQL -> oci_psql_db_system,
		// MySQL -> oci_mysql_mysql_db_system. Both are encrypted at rest by default.
		if engine == DBEngineMySQL {
			plan.ResourceType = "oci_mysql_mysql_db_system"
		} else {
			plan.ResourceType = "oci_psql_db_system"
		}
	case ProviderIBM:
		plan.ResourceType = "ibm_database"
	case ProviderAlibaba:
		plan.ResourceType = "alicloud_db_instance"
	case ProviderOVH:
		plan.ResourceType = "ovh_cloud_project_database"
	}
	return plan, nil
}

// canonicalEngine maps accepted engine aliases to the canonical token. Empty
// defaults to postgres (the PyxCloud default engine).
func canonicalEngine(e string) string {
	switch strings.ToLower(strings.TrimSpace(e)) {
	case DBEngineMySQL, "mariadb":
		return DBEngineMySQL
	case DBEnginePostgres, "postgresql", "pg":
		return DBEnginePostgres
	default:
		return DBEnginePostgres
	}
}

func validateManagedDatabaseSpec(spec ManagedDatabaseSpec) error {
	if strings.TrimSpace(spec.Region) == "" {
		return fmt.Errorf("managed-database: region (abstract pyx region_name) is required")
	}
	if strings.TrimSpace(spec.Provider) == "" {
		return fmt.Errorf("managed-database: provider is required (aws | gcp | digitalocean)")
	}
	if _, ok := ProviderToCSP(spec.Provider); !ok {
		return fmt.Errorf("managed-database: unknown provider %q (aws | gcp | digitalocean)", spec.Provider)
	}
	if e := strings.ToLower(strings.TrimSpace(spec.Engine)); e != "" {
		switch e {
		case DBEnginePostgres, "postgresql", "pg", DBEngineMySQL, "mariadb":
		default:
			return fmt.Errorf("managed-database: invalid engine %q (postgres | mysql)", spec.Engine)
		}
	}
	if spec.CPU < 1 {
		return fmt.Errorf("managed-database: cpu must be >= 1, got %d", spec.CPU)
	}
	if spec.RAM < 1 {
		return fmt.Errorf("managed-database: ram (GiB) must be >= 1, got %d", spec.RAM)
	}
	if spec.StorageGB < 0 {
		return fmt.Errorf("managed-database: storage_gb must be >= 0 (0 defaults to the %dGiB minimum), got %d", MinStorageGB, spec.StorageGB)
	}
	return nil
}

// ── DATA-SAFETY GUARD (SPEC §5.6) ────────────────────────────────────────────
//
// Why this is special: on AWS RDS (and the GCP/DO analogues) certain attribute
// changes are NOT applied in place — they FORCE A REPLACEMENT of the DB instance,
// which destroys the data. The 2026-06-15 RDS data-loss incident was exactly this:
// a flag flip that Terraform happily planned as a replace, silently dropping the
// production database. The guard detects these changes at PLAN TIME by diffing the
// PRIOR plan (state) against the NEW plan and raises a clear error instructing the
// operator to use a snapshot-restore migration instead. It NEVER silently proceeds.
//
// The replacement-forcing attributes for a managed-database are:
//   - encrypted          (RDS storage_encrypted is immutable; enabling it requires
//                          a copy-snapshot-with-KMS → restore, never an in-place flip)
//   - engine             (a different engine is a different DB; engine downgrade /
//                          cross-engine change forces replacement)
//   - identifier         (db_name / identifier change replaces the instance)
//   - storage type       (we model this via the class family token: a family change
//                          that maps to a different underlying storage type forces
//                          replacement on RDS, e.g. magnetic↔gp3 lineage)
//
// A SIZE change (db_class within the same family / storage GiB increase / HA
// toggle) is an in-place modify on all three providers and is allowed.

// DataSafetyViolation describes one replacement-forcing change the guard blocked.
type DataSafetyViolation struct {
	Attribute string // the changed attribute, e.g. "encrypted"
	From      string // prior value (state)
	To        string // new value (plan)
}

// ErrDataSafetyForceReplace is the hard plan-time error the data-safety guard
// raises when a change to an EXISTING managed-database would force its replacement
// (and thus destroy its data). It lists every offending change and directs the
// operator to the snapshot-restore migration path — it is never a silent proceed.
type ErrDataSafetyForceReplace struct {
	DBName     string
	Provider   string
	Violations []DataSafetyViolation
}

func (e ErrDataSafetyForceReplace) Error() string {
	var changes []string
	for _, v := range e.Violations {
		changes = append(changes, fmt.Sprintf("%s: %q -> %q", v.Attribute, v.From, v.To))
	}
	return fmt.Sprintf(
		"managed-database %q (%s): the following change(s) would FORCE-REPLACE the existing "+
			"database and DESTROY its data: [%s]. PyxCloud refuses to plan a destructive "+
			"replacement of a live database (this is the data-safety guard added after the "+
			"2026-06-15 RDS data-loss incident, where a flag flip was silently planned as a "+
			"replace and dropped a production DB). To apply this change safely, perform a "+
			"snapshot-restore migration: take a snapshot of the current DB, restore it into a "+
			"NEW managed-database with the desired settings (for encryption: "+
			"copy-snapshot-with-KMS then restore), cut traffic over, then retire the old one. "+
			"This is a hard plan-time error, never a silent replacement.",
		e.DBName, e.Provider, strings.Join(changes, "; "),
	)
}

// CheckManagedDatabaseDataSafety is the data-safety guard. Given the PRIOR plan
// (from state; nil on first create) and the NEW plan, it returns
// ErrDataSafetyForceReplace if any replacement-forcing attribute changed on an
// EXISTING database. On a fresh create (prior == nil) it is always safe. Size /
// storage-increase / HA changes are in-place and pass.
//
// This is the function the resource's ModifyPlan calls at plan time so the error
// surfaces in `terraform plan`, before any apply touches the live DB.
func CheckManagedDatabaseDataSafety(prior, next *ManagedDatabasePlan) error {
	if prior == nil || next == nil {
		return nil // fresh create (or destroy): nothing to force-replace
	}

	var violations []DataSafetyViolation

	// Encryption flip on an existing DB forces replacement (RDS storage_encrypted
	// is immutable). Both enabling and disabling are blocked — go via snapshot.
	if prior.Encrypted != next.Encrypted {
		violations = append(violations, DataSafetyViolation{
			Attribute: "encrypted",
			From:      boolStr(prior.Encrypted),
			To:        boolStr(next.Encrypted),
		})
	}
	// Engine change (incl. downgrade / cross-engine) forces replacement.
	if !strings.EqualFold(prior.Engine, next.Engine) {
		violations = append(violations, DataSafetyViolation{
			Attribute: "engine",
			From:      prior.Engine,
			To:        next.Engine,
		})
	}
	// Identifier (logical DB name / RDS identifier) change forces replacement.
	if prior.DBName != next.DBName {
		violations = append(violations, DataSafetyViolation{
			Attribute: "identifier",
			From:      prior.DBName,
			To:        next.DBName,
		})
	}
	// Storage-type change: modelled via the class family token. A family change
	// that crosses a storage-type lineage forces replacement on RDS. We treat any
	// family change as replacement-forcing for safety (the catalog family token is
	// the storage/class lineage); a same-family resize is in-place and passes.
	if !strings.EqualFold(prior.Family, next.Family) {
		violations = append(violations, DataSafetyViolation{
			Attribute: "storage_type/class_family",
			From:      prior.Family,
			To:        next.Family,
		})
	}

	if len(violations) > 0 {
		return ErrDataSafetyForceReplace{
			DBName:     next.DBName,
			Provider:   next.Provider,
			Violations: violations,
		}
	}
	return nil
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// nearestDBSizes returns up to n rows from the candidate set sorted by distance
// from the requested (cpu, ram) — used to populate the no-match error message.
func nearestDBSizes(candidates []MDBRow, cpu, ram, n int) []MDBRow {
	type scored struct {
		row  MDBRow
		dist int
	}
	scoredRows := make([]scored, 0, len(candidates))
	for _, r := range candidates {
		d := abs(r.CPU-cpu)*4 + abs(r.RAM-ram)
		scoredRows = append(scoredRows, scored{row: r, dist: d})
	}
	sort.SliceStable(scoredRows, func(i, j int) bool {
		if scoredRows[i].dist != scoredRows[j].dist {
			return scoredRows[i].dist < scoredRows[j].dist
		}
		return scoredRows[i].row.Name < scoredRows[j].row.Name
	})
	out := make([]MDBRow, 0, n)
	for i := 0; i < len(scoredRows) && i < n; i++ {
		out = append(out, scoredRows[i].row)
	}
	return out
}
