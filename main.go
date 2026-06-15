// Command terraform-provider-pyxcloud is the Terraform provider plugin entry
// point for the PyxCloud platform.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/provider"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
)

// version is overridden at build time via -ldflags.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers")
	flag.Parse()

	opts := providerserver.ServeOpts{
		Address: "registry.terraform.io/PyxCloud/pyxcloud",
		Debug:   debug,
	}

	if err := providerserver.Serve(context.Background(), provider.New(version), opts); err != nil {
		log.Fatal(err.Error())
	}
}
