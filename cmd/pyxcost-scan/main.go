// Command pyxcost-scan reads a billing cost snapshot (JSON) and applies the
// built-in cost-blowout signal rules. It exits 0 when no rules fire and 1 when
// at least one signal is triggered, making it suitable as a CI gate step.
//
// Usage:
//
//	pyxcost-scan -in billing.json            # scan a file
//	cat billing.json | pyxcost-scan          # scan from stdin
//	pyxcost-scan -in billing.json -json      # machine-readable JSON result
//
// Input JSON schema (billing.json):
//
//	{
//	  "current":  { "compute": 11000.0, "storage": 500.0 },
//	  "baseline": { "compute":  8000.0, "storage": 490.0 }
//	}
//
// "current" is the resource-type → current monthly cost map (USD).
// "baseline" is the resource-type → previous/expected monthly cost map (USD).
// Both fields are optional but omitting "baseline" disables spike/anomaly rules.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/billingscan"
)

func main() {
	in := flag.String("in", "", "path to billing JSON input (omit to read stdin)")
	asJSON := flag.Bool("json", false, "emit machine-readable JSON result to stdout")
	flag.Parse()

	var result billingscan.Result
	var err error

	if *in != "" {
		result, err = billingscan.ScanFile(*in)
	} else {
		result, err = billingscan.ScanReader(os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyxcost-scan:", err)
		os.Exit(2)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, "pyxcost-scan: encode result:", err)
			os.Exit(2)
		}
		if !result.OK {
			os.Exit(1)
		}
		return
	}

	// Human-readable output.
	if result.OK {
		fmt.Println("pyxcost-scan: OK — no cost-blowout signals triggered")
		os.Exit(0)
	}

	fmt.Fprintf(os.Stderr, "pyxcost-scan: COST BLOWOUT — %d signal(s) triggered:\n", len(result.Signals))
	for i, sig := range result.Signals {
		fmt.Fprintf(os.Stderr, "  [%d] rule=%s type=%s message=%q\n",
			i+1, sig.RuleID, sig.RuleType, sig.Message)
	}
	os.Exit(1)
}
