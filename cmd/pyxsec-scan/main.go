// Command pyxsec-scan reads a list of Terraform resources (JSON) and applies
// all built-in IaC security checks. It exits 0 when no HIGH-severity findings
// are detected and 1 when at least one HIGH-severity finding is triggered,
// making it suitable as a CI gate step.
//
// Usage:
//
//	pyxsec-scan -in resources.json        # scan a file
//	cat resources.json | pyxsec-scan      # scan from stdin
//	pyxsec-scan -in resources.json -json  # machine-readable JSON result
//
// Input JSON schema (resources.json):
//
//	{
//	  "resources": [
//	    {
//	      "type": "aws_security_group_rule",
//	      "attributes": {
//	        "type": "ingress",
//	        "from_port": 22,
//	        "to_port": 22,
//	        "protocol": "tcp",
//	        "cidr_blocks": ["0.0.0.0/0"]
//	      }
//	    }
//	  ]
//	}
//
// Checks performed (advisory findings, HIGH blocks gate):
//   - OPEN-PORT-SGR / OPEN-PORT-SG: public ingress (0.0.0.0/0) on sensitive ports
//   - PUBLIC-STORAGE-ACL / PUBLIC-STORAGE-BLOCK / PUBLIC-STORAGE-POLICY: public S3
//   - IAM-WILDCARD / IAM-WILDCARD-ARRAY: wildcard Action + Resource IAM policies
//   - UNENCRYPTED-EBS / UNENCRYPTED-RDS / UNENCRYPTED-RDS-CLUSTER: unencrypted storage
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/PyxCloud/terraform-provider-pyxcloud/internal/iacsecscan"
)

func main() {
	in := flag.String("in", "", "path to resources JSON input (omit to read stdin)")
	asJSON := flag.Bool("json", false, "emit machine-readable JSON result to stdout")
	flag.Parse()

	var result iacsecscan.ScanResult
	var err error

	if *in != "" {
		result, err = iacsecscan.ScanFile(*in)
	} else {
		result, err = iacsecscan.ScanReader(os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pyxsec-scan:", err)
		os.Exit(2)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			fmt.Fprintln(os.Stderr, "pyxsec-scan: encode result:", err)
			os.Exit(2)
		}
		if !result.OK {
			os.Exit(1)
		}
		return
	}

	// Human-readable output.
	if result.OK {
		if len(result.Findings) == 0 {
			fmt.Println("pyxsec-scan: OK — no security findings detected")
		} else {
			fmt.Printf("pyxsec-scan: OK — %d advisory finding(s) (no HIGH severity)\n", len(result.Findings))
			for i, f := range result.Findings {
				fmt.Printf("  [%d] [%s] rule=%s resource=%s/%s: %s\n",
					i+1, f.Severity, f.RuleID, f.ResourceType, f.ResourceName, f.Description)
			}
		}
		os.Exit(0)
	}

	fmt.Fprintf(os.Stderr, "pyxsec-scan: SECURITY GATE BLOCKED — %d finding(s):\n", len(result.Findings))
	for i, f := range result.Findings {
		fmt.Fprintf(os.Stderr, "  [%d] [%s] rule=%s resource=%s/%s: %s\n",
			i+1, f.Severity, f.RuleID, f.ResourceType, f.ResourceName, f.Description)
		fmt.Fprintf(os.Stderr, "       remediation: %s\n", f.Remediation)
	}
	os.Exit(1)
}
