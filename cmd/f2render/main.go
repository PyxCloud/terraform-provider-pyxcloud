// Command f2render renders the DO account-baseline (the F2-02 cutover estate,
// state cutover/do-baseline-fra1.tfstate) to concrete DigitalOcean terraform with
// the six per-service bootstraps wired in. It is the render path the F2-02 landing
// used: DOBaselineComponentsWithDOBootstraps -> AssembleHCL, with the estate
// name/region/CIDR/expose reconstructed to match the LIVE state so the only plan
// delta is the four fixed pools' user_data.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
)

func main() {
	out := flag.String("out", "out", "output dir for the rendered .tf")
	flag.Parse()

	// SSO inlines its secret VALUES into user_data (no ${var} refs). To keep the
	// sso pool's rendered user_data byte-identical to the live state we DON'T
	// re-render it here from possibly-different secret values; instead we only
	// render/apply the four fixed pools (mcp/obs/backend/vpn). But AssembleHCL
	// needs a valid sso spec to render the estate, so we feed the sso values from
	// env (populated from Secrets Manager by the caller) purely so the render
	// succeeds; the sso pool is NOT in the apply -target set.
	e := os.Getenv
	specs := catalog.DOBootstrapSpecs{
		MCP: catalog.McpDOBootstrapSpec{Environment: "beta"},
		SSO: catalog.SSODOBootstrapSpec{
			Environment:     "beta",
			KCDBURL:         e("SSO_KCDBURL"),
			KCDBUsername:    e("SSO_KCDBUSER"),
			KCDBPassword:    e("SSO_KCDBPASS"),
			AdminPassword:   e("SSO_ADMINPASS"),
			VaultOIDCSecret: e("SSO_VAULTOIDC"),
			SpacesAccessKey: e("SPACES_ACCESS_KEY"),
			SpacesSecretKey: e("SPACES_SECRET_KEY"),
		},
		OBS:     catalog.OBSDOBootstrapSpec{},
		SAST:    catalog.SastDOBootstrapSpec{Environment: "beta"},
		Backend: catalog.BackendBootstrapSpec{Environment: "beta"},
		VPN:     catalog.VPNBootstrapSpec{Environment: "beta"},
	}

	comps, err := catalog.DOBaselineComponentsWithDOBootstraps("", "", "", specs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "baseline:", err)
		os.Exit(1)
	}

	in := catalog.AssembleInput{
		Name:       "passo-do-baseline",
		Provider:   "digitalocean",
		Region:     "Frankfurt", // canonical region_name -> DO fra1
		CIDR:       "10.0.1.0/24",
		Subnets:    []string{"10.0.1.0/24"},
		Expose:     []int{443},
		Components: comps,
	}

	cat, err := catalog.NewEmbedded()
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		os.Exit(1)
	}
	docs, err := catalog.AssembleHCL(context.Background(), cat, in)
	if err != nil {
		fmt.Fprintln(os.Stderr, "assemble:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for i, d := range docs {
		name := filepath.Join(*out, fmt.Sprintf("pyx-%02d.tf", i+1))
		if err := os.WriteFile(name, []byte(d), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	fmt.Printf("rendered %d document(s) to %s\n", len(docs), *out)
}
