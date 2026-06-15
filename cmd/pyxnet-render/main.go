// Command pyxnet-render renders a canonical PyxCloud network fixture into
// concrete cloud-provider Terraform HCL via the catalog. It is the bridge used
// by the per-provider `terraform plan` / real apply round-trip tests (SPEC §6):
// generate the provider config from a canonical fixture, then plan/apply it.
//
// Usage:
//
//	pyxnet-render -fixture place.json -provider aws   > aws_vpc.tf
//	pyxnet-render -fixture place.json -provider gcp   > gcp_vpc.tf
//	pyxnet-render -fixture place.json -provider digitalocean > do_vpc.tf
//
// The fixture is the abstract, provider-neutral place; -provider selects which
// concrete provider to descend it to.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
)

// fixture is the canonical, provider-neutral place network description.
type fixture struct {
	Name    string   `json:"name"`
	Region  string   `json:"region"`
	CIDR    string   `json:"cidr"`
	Subnets []string `json:"subnets"`
}

func main() {
	fixturePath := flag.String("fixture", "", "path to canonical network fixture JSON")
	provider := flag.String("provider", "", "target provider: aws | gcp | digitalocean")
	flag.Parse()

	if *fixturePath == "" || *provider == "" {
		fmt.Fprintln(os.Stderr, "usage: pyxnet-render -fixture place.json -provider aws|gcp|digitalocean")
		os.Exit(2)
	}

	raw, err := os.ReadFile(*fixturePath)
	if err != nil {
		fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		fatal(fmt.Errorf("parse fixture: %w", err))
	}

	cat := catalog.MustEmbedded()
	plan, err := catalog.TranslateNetwork(context.Background(), cat, catalog.NetworkSpec{
		Name:     f.Name,
		Region:   f.Region,
		Provider: *provider,
		CIDR:     f.CIDR,
		Subnets:  f.Subnets,
	})
	if err != nil {
		fatal(err)
	}
	hcl, err := catalog.RenderHCL(plan)
	if err != nil {
		fatal(err)
	}
	fmt.Print(hcl)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "pyxnet-render:", err)
	os.Exit(1)
}
