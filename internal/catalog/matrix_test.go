package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCatalogSizingMatrix(t *testing.T) {
	// Bypass JIT live checks during catalog matrix test to keep it pure and fast.
	t.Setenv("PYXCLOUD_BYPASS_JIT_CHECK", "true")

	cat := MustEmbedded()
	ctx := context.Background()

	// List of CSP tokens we want to test
	csps := []string{
		"aws",
		"azure",
		"gcp",
		"alicloud",
		"oci",
		"ovh",
		"ubicloud",
	}

	// Sizing pairs to verify (standard vCPU/RAM sizes from 1 to 64 CPU, 1GiB to 512GiB RAM)
	cpuValues := []int{1, 2, 4, 8, 16, 32, 64}
	ramValues := []int{1, 2, 4, 8, 16, 32, 64, 128, 256, 512}

	architectures := []string{ArchX8664, ArchARM64}

	for _, csp := range csps {
		// Gather distinct regions for this CSP
		regions := make(map[string]struct{})
		for _, row := range cat.Rows() {
			if row.CSP == csp {
				regions[row.CSPRegion] = struct{}{}
			}
		}

		if len(regions) == 0 {
			t.Errorf("CSP %q has no regions configured in the embedded catalog", csp)
			continue
		}

		for region := range regions {
			for _, arch := range architectures {
				for _, cpu := range cpuValues {
					for _, ram := range ramValues {
						t.Run(fmt.Sprintf("%s/%s/%s/%dvCPU-%dGiB", csp, region, arch, cpu, ram), func(t *testing.T) {
							sku, err := cat.ResolveSKU(ctx, csp, region, arch, cpu, ram)
							if err != nil {
								// Must return a predictable ErrSKUNotFound if the SKU doesn't exist
								var notFound ErrSKUNotFound
								if !errors.As(err, &notFound) {
									t.Fatalf("unexpected error type for missing SKU: %v", err)
								}
								// Verify that the error metadata matches the requested sizing
								if notFound.CSP != csp || notFound.CSPRegion != region || notFound.Architecture != arch || notFound.CPU != cpu || notFound.RAM != ram {
									t.Errorf("mismatched metadata in ErrSKUNotFound: got %+v", notFound)
								}
							} else {
								// If found, verify that it matches exactly what was requested
								if sku.CSP != csp {
									t.Errorf("mismatched CSP: got %q, want %q", sku.CSP, csp)
								}
								if sku.CSPRegion != region {
									t.Errorf("mismatched region: got %q, want %q", sku.CSPRegion, region)
								}
								if sku.Architecture != arch {
									t.Errorf("mismatched architecture: got %q, want %q", sku.Architecture, arch)
								}
								if sku.CPU != cpu {
									t.Errorf("mismatched CPU: got %d, want %d", sku.CPU, cpu)
								}
								if sku.RAM != ram {
									t.Errorf("mismatched RAM: got %d, want %d", sku.RAM, ram)
								}
								if sku.Name == "" {
									t.Error("resolved SKU name is empty")
								}
							}
						})
					}
				}
			}
		}
	}
}

func TestResolveSKUJITAWS(t *testing.T) {
	// Create a temp directory for our mock binaries
	tmpDir := t.TempDir()

	// Write mock 'aws' script that simulates get-caller-identity and describe-instance-type-offerings
	awsScript := `#!/bin/sh
if [ "$1" = "sts" ] && [ "$2" = "get-caller-identity" ]; then
    exit 0
fi
if [ "$1" = "ec2" ] && [ "$2" = "describe-instance-type-offerings" ]; then
    case "$*" in
        *t3.medium*)
            echo '{"InstanceTypeOfferings": [{"InstanceType": "t3.medium", "Location": "eu-west-1"}]}'
            ;;
        *)
            echo '{"InstanceTypeOfferings": []}'
            ;;
    esac
    exit 0
fi
exit 1
`
	awsPath := filepath.Join(tmpDir, "aws")
	if err := os.WriteFile(awsPath, []byte(awsScript), 0755); err != nil {
		t.Fatalf("failed to write mock aws script: %v", err)
	}

	// Update PATH to prioritize our mock binary
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", tmpDir + ":" + origPath)
	t.Setenv("PYXCLOUD_BYPASS_JIT_CHECK", "false")

	cat := MustEmbedded()
	ctx := context.Background()

	// 1. Success case: resolving a size that maps to t3.medium (which mock aws says is offered)
	sku, err := cat.ResolveSKU(ctx, "aws", "eu-west-1", "x86_64", 2, 4)
	if err != nil {
		t.Fatalf("ResolveSKU failed: %v", err)
	}
	if sku.Name != "t3.medium" {
		t.Errorf("resolved SKU = %q, want t3.medium", sku.Name)
	}

	// 2. Failure case: resolving a size that maps to t3.micro (which mock aws says is NOT offered)
	// Dublin -> eu-west-1; 2 vCPU / 1 GiB x86_64 -> t3.micro.
	_, err = cat.ResolveSKU(ctx, "aws", "eu-west-1", "x86_64", 2, 1)
	if err == nil {
		t.Fatal("expected JIT validation failure for t3.micro, got nil")
	}
	if !strings.Contains(err.Error(), "JIT validation failed") {
		t.Errorf("expected JIT validation failure message, got: %v", err)
	}
}
