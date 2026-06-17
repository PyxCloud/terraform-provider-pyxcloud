// Command pyxenv-render renders a canonical environment (JSON catalog.AssembleInput)
// to concrete provider terraform locally — the plan-only / CI counterpart of the
// pyxcloud_environment resource's apply-time translation. It writes one .tf file per
// document into an output dir, so you can `terraform init && terraform plan` the
// result against a real cloud WITHOUT applying.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/catalog"
)

func main() {
	in := flag.String("in", "", "path to a JSON catalog.AssembleInput")
	out := flag.String("out", "out", "output dir for the rendered .tf")
	flag.Parse()
	if *in == "" {
		fmt.Fprintln(os.Stderr, "usage: pyxenv-render -in env.json -out dir")
		os.Exit(2)
	}
	raw, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var input catalog.AssembleInput
	if err := json.Unmarshal(raw, &input); err != nil {
		fmt.Fprintln(os.Stderr, "decode:", err)
		os.Exit(1)
	}
	cat, err := catalog.NewEmbedded()
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		os.Exit(1)
	}
	docs, err := catalog.AssembleHCL(context.Background(), cat, input)
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
