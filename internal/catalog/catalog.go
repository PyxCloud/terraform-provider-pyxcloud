// Package catalog provides catalog-driven resolution of PyxCloud's abstract
// region vocabulary into concrete cloud-provider regions and zones.
//
// This is the provider-side mirror of the backend's RegionResolver
// (io.pyxcloud.service.pyxfile.RegionResolver) + the `region` catalog table
// (macro_region / country / region_name / csp_region / csp / csp_region_description).
//
// IMPORTANT: the provider does NOT invent region maps. The resolution data is
// the catalog itself — the materialized `region` table. The default
// implementation (EmbeddedCatalog) is seeded from a snapshot of that table
// (region_catalog.csv, the wave-1 aws/gcp/do rows of the live join the wizard
// and Compare page use). A future BackendCatalog will fetch the same rows live
// over HTTP from the PyxCloud BE (GET /api/catalog/regions) — see catalog_backend.go.
package catalog

import (
	"context"
	"fmt"
	"strings"
)

// CSP tokens as used INSIDE the PyxCloud catalog (`region.csp`). Note these
// differ from the provider-facing names: the catalog uses "do", the provider
// (and Terraform) speak "digitalocean".
const (
	cspAWS = "aws"
	cspGCP = "gcp"
	cspDO  = "do"
	// cspLinode is the wave-2 Linode (Akamai) catalog token. Linode rows are
	// folded into the embedded catalog from linode_catalog.csv (see embedded.go).
	cspLinode = "linode"
	// cspOracle is the catalog csp token for Oracle Cloud Infrastructure (wave-2).
	// The provider-facing name is "oracle"; the catalog token is "oci".
	cspOracle = "oci"
	cspIBM    = "ibm" // wave-2: IBM Cloud (IBM-Cloud/ibm provider)
	// cspAlibaba is the catalog csp token for Alibaba Cloud (wave-2). The catalog
	// rows and the Terraform `alicloud` provider both speak "alicloud", so unlike
	// "do"/"digitalocean" the provider-facing name and the csp token coincide.
	cspAlibaba = "alicloud"
)

// Provider-facing names (Terraform `provider` attribute / ENABLED_LAUNCH_PROVIDERS).
const (
	ProviderAWS          = "aws"
	ProviderGCP          = "gcp"
	ProviderDigitalOcean = "digitalocean"
	// ProviderLinode is the wave-2 Linode (Akamai) provider — the `linode/linode`
	// Terraform provider. Its catalog token is the same string ("linode").
	ProviderLinode = "linode"
	// ProviderOracle is the wave-2 Oracle Cloud Infrastructure provider. It
	// descends to the oracle/oci Terraform provider (oci_* resources). The
	// provider-facing name is "oracle"; the catalog csp token is "oci".
	ProviderOracle = "oracle"
	// ProviderIBM is the wave-2 IBM Cloud provider. The provider-facing name and
	// the catalog csp token are both "ibm" (unlike DigitalOcean, where the
	// provider speaks "digitalocean" but the catalog csp token is "do").
	ProviderIBM = "ibm"
	// ProviderAlibaba is the wave-2 Alibaba Cloud provider (pd-TF-PROVIDERS-WAVE2:
	// alibaba). The Terraform provider name is "alicloud" (the official
	// aliyun/alicloud provider), matching the catalog csp token.
	ProviderAlibaba = "alicloud"
)

// providerToCSP maps a Terraform-facing provider name to the catalog csp token.
// ProviderAzure (wave-2) is defined in render_azure.go and registered here.
// ProviderUbicloud (wave-2) is defined in render_ubicloud.go and registered here.
var providerToCSP = map[string]string{
	ProviderAWS:          cspAWS,
	ProviderGCP:          cspGCP,
	ProviderDigitalOcean: cspDO,
	ProviderAzure:        cspAzure,
	ProviderLinode:       cspLinode,
	ProviderUbicloud:     cspUbicloud,
	ProviderOracle:       cspOracle,
	ProviderIBM:          cspIBM,
	ProviderAlibaba:      cspAlibaba,
}

// ProviderToCSP returns the catalog csp token for a provider-facing name, and
// whether the provider is a known wave-1 launch provider.
func ProviderToCSP(provider string) (string, bool) {
	csp, ok := providerToCSP[strings.ToLower(strings.TrimSpace(provider))]
	return csp, ok
}

// RegionRow mirrors one row of the backend `region` table.
type RegionRow struct {
	MacroRegion          string
	Country              string
	RegionName           string // abstract pyx region the user picks, e.g. "Dublin"
	CSPRegion            string // concrete provider region, e.g. "eu-west-1"
	CSPRegionDescription string
	CSP                  string // catalog csp token: aws | gcp | do
}

// RegionCatalog is the resolution boundary the provider depends on. Both the
// embedded snapshot and a live-BE client satisfy it, so the provider never
// embeds region logic of its own.
type RegionCatalog interface {
	// ResolveRegion resolves an abstract pyx region_name + provider-facing name
	// into the catalog row carrying the concrete csp_region. It returns
	// ErrRegionNotFound when the catalog has no entry for that pair — never a
	// silent fallback.
	ResolveRegion(ctx context.Context, regionName, provider string) (RegionRow, error)
}

// Catalog is the full resolution boundary the provider depends on: region + VM
// SKU/image + managed-database class resolution. Both the embedded snapshot and
// the live-BE client satisfy it, so the provider holds one catalog handle that
// every component translation resolves against (no per-component plumbing).
type Catalog interface {
	VMCatalog
	MDBCatalog
}

var (
	_ Catalog = (*EmbeddedCatalog)(nil)
	_ Catalog = (*BackendCatalog)(nil)
)

// ErrRegionNotFound is returned when no catalog row matches (region_name, provider).
type ErrRegionNotFound struct {
	RegionName string
	Provider   string
}

func (e ErrRegionNotFound) Error() string {
	return fmt.Sprintf(
		"no catalog region for region_name=%q provider=%q: the PyxCloud region catalog "+
			"has no csp_region for this pair. Pick a region_name available for this provider "+
			"(this is a hard plan-time error, never a silent fallback)",
		e.RegionName, e.Provider,
	)
}
