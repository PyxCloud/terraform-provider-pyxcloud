package catalog

import (
	"context"
	"fmt"
	"strings"
)

// Topology is a full canonical place: the components to provision together. Each
// field is an already-formed Spec (the caller sets Provider/Region on each). It is
// the importable, reusable analogue of cmd/pyxnet-render's fixture — so the
// terraform provider can translate a whole topology to concrete .tf LOCALLY
// (Mode A), driving `terraform apply` with the catalog coverage, without a backend
// round-trip.
type Topology struct {
	Network         *NetworkSpec
	SecurityGroup   *SecurityGroupSpec
	VirtualMachine  *VMSpec
	ScaleGroup      *ScaleGroupSpec
	LoadBalancer    *LoadBalancerSpec
	ManagedDatabase *ManagedDatabaseSpec
	ObjectStorage   *ObjectStorageSpec
	Cache           *CacheSpec
	Secrets         *SecretsSpec

	// First-class components added for the our-repos migration (global / no SKU).
	IAM           []IAMSpec
	Observability *ObservabilitySpec
	KMS           *KMSSpec
	CloudflareDNS *CloudflareDNSSpec
	BlockStorage  *BlockStorageSpec
	Email         *EmailSpec
	PrefixList    []PrefixListSpec
	Canary        []CanarySpec
}

// AssembleHCL translates every present component of the topology and concatenates
// the rendered concrete .tf, in a deterministic order. Catalog-dependent
// components (region/SKU) use cat; global components (iam/kms/email/…) do not. A
// failure on any component is a hard error naming the component (never a silent
// skip), so a topology either renders completely or fails loud — exactly the
// guarantee a `terraform apply` replacement needs.
func AssembleHCL(ctx context.Context, cat Catalog, topo Topology) (string, error) {
	var parts []string
	add := func(component, hcl string, err error) error {
		if err != nil {
			return fmt.Errorf("%s: %w", component, err)
		}
		if s := strings.TrimSpace(hcl); s != "" {
			parts = append(parts, s)
		}
		return nil
	}

	// Catalog-dependent components first (network/SG must precede the resources
	// that reference them), then the rest.
	if topo.Network != nil {
		plan, err := TranslateNetwork(ctx, cat, *topo.Network)
		if err == nil {
			var hcl string
			hcl, err = RenderHCL(plan)
			if e := add("network", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("network", "", err); e != nil {
			return "", e
		}
	}
	if topo.SecurityGroup != nil {
		plan, err := TranslateSecurityGroup(ctx, cat, *topo.SecurityGroup)
		if err == nil {
			var hcl string
			hcl, err = RenderSGHCL(plan)
			if e := add("security-group", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("security-group", "", err); e != nil {
			return "", e
		}
	}
	if topo.IAM != nil {
		for i := range topo.IAM {
			plan, err := TranslateIAM(topo.IAM[i])
			if err == nil {
				var hcl string
				hcl, err = RenderIAMHCL(plan)
				if e := add("iam", hcl, err); e != nil {
					return "", e
				}
			} else if e := add("iam", "", err); e != nil {
				return "", e
			}
		}
	}
	if topo.KMS != nil {
		plan, err := TranslateKMS(*topo.KMS)
		if err == nil {
			var hcl string
			hcl, err = RenderKMSHCL(plan)
			if e := add("kms", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("kms", "", err); e != nil {
			return "", e
		}
	}
	if topo.VirtualMachine != nil {
		plan, err := TranslateVM(ctx, cat, *topo.VirtualMachine)
		if err == nil {
			var hcl string
			hcl, err = RenderVMHCL(plan)
			if e := add("virtual-machine", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("virtual-machine", "", err); e != nil {
			return "", e
		}
	}
	if topo.ScaleGroup != nil {
		plan, err := TranslateScaleGroup(ctx, cat, *topo.ScaleGroup)
		if err == nil {
			var hcl string
			hcl, err = RenderScaleGroupHCL(plan)
			if e := add("scale-group", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("scale-group", "", err); e != nil {
			return "", e
		}
	}
	if topo.LoadBalancer != nil {
		plan, err := TranslateLoadBalancer(ctx, cat, *topo.LoadBalancer)
		if err == nil {
			var hcl string
			hcl, err = RenderLoadBalancerHCL(plan)
			if e := add("load-balancer", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("load-balancer", "", err); e != nil {
			return "", e
		}
	}
	if topo.ManagedDatabase != nil {
		plan, err := TranslateManagedDatabase(ctx, cat, *topo.ManagedDatabase)
		if err == nil {
			var hcl string
			hcl, err = RenderManagedDatabaseHCL(plan)
			if e := add("managed-database", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("managed-database", "", err); e != nil {
			return "", e
		}
	}
	if topo.ObjectStorage != nil {
		plan, err := TranslateObjectStorage(ctx, cat, *topo.ObjectStorage)
		if err == nil {
			var hcl string
			hcl, err = RenderObjectStorageHCL(plan)
			if e := add("object-storage", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("object-storage", "", err); e != nil {
			return "", e
		}
	}
	if topo.BlockStorage != nil {
		plan, err := TranslateBlockStorage(*topo.BlockStorage)
		if err == nil {
			var hcl string
			hcl, err = RenderBlockStorageHCL(plan)
			if e := add("block-storage", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("block-storage", "", err); e != nil {
			return "", e
		}
	}
	if topo.Cache != nil {
		plan, err := TranslateCache(ctx, cat, *topo.Cache)
		if err == nil {
			var hcl string
			hcl, err = RenderCacheHCL(plan)
			if e := add("cache", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("cache", "", err); e != nil {
			return "", e
		}
	}
	if topo.Secrets != nil {
		plan, err := TranslateSecrets(ctx, cat, *topo.Secrets)
		if err == nil {
			var hcl string
			hcl, err = RenderSecretsHCL(plan)
			if e := add("secrets", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("secrets", "", err); e != nil {
			return "", e
		}
	}
	if topo.Observability != nil {
		plan, err := TranslateObservability(*topo.Observability)
		if err == nil {
			var hcl string
			hcl, err = RenderObservabilityHCL(plan)
			if e := add("observability", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("observability", "", err); e != nil {
			return "", e
		}
	}
	if topo.Email != nil {
		plan, err := TranslateEmail(*topo.Email)
		if err == nil {
			var hcl string
			hcl, err = RenderEmailHCL(plan)
			if e := add("email", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("email", "", err); e != nil {
			return "", e
		}
	}
	if topo.CloudflareDNS != nil {
		plan, err := TranslateCloudflareDNS(*topo.CloudflareDNS)
		if err == nil {
			var hcl string
			hcl, err = RenderCloudflareDNSHCL(plan)
			if e := add("cloudflare-dns", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("cloudflare-dns", "", err); e != nil {
			return "", e
		}
	}
	for i := range topo.PrefixList {
		plan, err := TranslatePrefixList(topo.PrefixList[i])
		if err == nil {
			var hcl string
			hcl, err = RenderPrefixListHCL(plan)
			if e := add("prefix-list", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("prefix-list", "", err); e != nil {
			return "", e
		}
	}
	for i := range topo.Canary {
		plan, err := TranslateCanary(topo.Canary[i])
		if err == nil {
			var hcl string
			hcl, err = RenderCanaryHCL(plan)
			if e := add("canary", hcl, err); e != nil {
				return "", e
			}
		} else if e := add("canary", "", err); e != nil {
			return "", e
		}
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("topology has no components to render")
	}
	return strings.Join(parts, "\n\n") + "\n", nil
}
