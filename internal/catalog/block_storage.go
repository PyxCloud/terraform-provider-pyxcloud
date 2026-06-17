package catalog

import (
	"fmt"
	"strings"
)

// BlockVolumeSpec is one block (EBS-style) data volume — persistent storage
// attached to a VM, distinct from object-storage. Used for stateful data dirs
// (the mesh data-lake, Vault data) that must survive instance replacement.
type BlockVolumeSpec struct {
	Name       string // logical volume name
	SizeGiB    int    // capacity
	Type       string // gp3 (default) | gp2 | io1 | ...
	Encrypted  bool
	AZ         string // availability zone (required on AWS; e.g. eu-west-1a)
	AttachTo   string // instance logical name to attach to (optional)
	DeviceName string // e.g. /dev/sdf (required when AttachTo set)
}

// BlockStorageSpec is the canonical block-storage component: a set of data volumes.
type BlockStorageSpec struct {
	Name     string
	Provider string
	Volumes  []BlockVolumeSpec
}

// BlockStoragePlan is the deterministic concrete translation.
type BlockStoragePlan struct {
	Provider     string            `json:"provider"`
	CSP          string            `json:"csp"`
	Name         string            `json:"name"`
	Volumes      []BlockVolumeSpec `json:"volumes"`
	ResourceType string            `json:"resource_type"`
}

// TranslateBlockStorage resolves a BlockStorageSpec into a concrete plan. AWS is
// fully supported; other providers are a hard "unsupported" error.
func TranslateBlockStorage(spec BlockStorageSpec) (BlockStoragePlan, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return BlockStoragePlan{}, fmt.Errorf("block-storage: name is required")
	}
	csp, ok := ProviderToCSP(spec.Provider)
	if !ok {
		return BlockStoragePlan{}, fmt.Errorf("block-storage: unknown provider %q", spec.Provider)
	}
	if len(spec.Volumes) == 0 {
		return BlockStoragePlan{}, fmt.Errorf("block-storage: declare at least one volume")
	}
	provider := strings.ToLower(strings.TrimSpace(spec.Provider))
	for i := range spec.Volumes {
		v := &spec.Volumes[i]
		if strings.TrimSpace(v.Name) == "" {
			return BlockStoragePlan{}, fmt.Errorf("block-storage: each volume needs a name")
		}
		if v.SizeGiB < 1 {
			return BlockStoragePlan{}, fmt.Errorf("block-storage: volume %q size must be >= 1 GiB", v.Name)
		}
		if provider == ProviderAWS && strings.TrimSpace(v.AZ) == "" {
			return BlockStoragePlan{}, fmt.Errorf("block-storage: volume %q needs an availability zone on AWS", v.Name)
		}
		if strings.TrimSpace(v.AttachTo) != "" && strings.TrimSpace(v.DeviceName) == "" {
			return BlockStoragePlan{}, fmt.Errorf("block-storage: volume %q attaches to %q but has no device_name", v.Name, v.AttachTo)
		}
		if strings.TrimSpace(v.Type) == "" {
			v.Type = "gp3"
		}
	}
	plan := BlockStoragePlan{Provider: provider, CSP: csp, Name: spec.Name, Volumes: spec.Volumes}
	switch provider {
	case ProviderAWS:
		plan.ResourceType = "aws_ebs_volume"
	default:
		return BlockStoragePlan{}, fmt.Errorf("block-storage: unsupported on provider %q (supported: aws EBS). "+
			"Hard plan-time error, never an invented resource", provider)
	}
	return plan, nil
}

// RenderBlockStorageHCL renders a resolved plan.
func RenderBlockStorageHCL(plan BlockStoragePlan) (string, error) {
	if plan.Provider != ProviderAWS {
		return "", fmt.Errorf("block-storage: no renderer for provider %q", plan.Provider)
	}
	var b strings.Builder
	for _, v := range plan.Volumes {
		rn := tfName(plan.Name + "-" + v.Name)
		fmt.Fprintf(&b, "resource \"aws_ebs_volume\" %q {\n", rn)
		fmt.Fprintf(&b, "  availability_zone = %q\n", v.AZ)
		fmt.Fprintf(&b, "  size              = %d\n", v.SizeGiB)
		fmt.Fprintf(&b, "  type              = %q\n", v.Type)
		fmt.Fprintf(&b, "  encrypted         = %t\n", v.Encrypted)
		fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", v.Name)
		b.WriteString("}\n\n")

		if v.AttachTo != "" {
			an := tfName(plan.Name + "-" + v.Name + "-att")
			fmt.Fprintf(&b, "resource \"aws_volume_attachment\" %q {\n", an)
			fmt.Fprintf(&b, "  device_name = %q\n", v.DeviceName)
			fmt.Fprintf(&b, "  volume_id   = aws_ebs_volume.%s.id\n", rn)
			fmt.Fprintf(&b, "  instance_id = aws_instance.%s.id\n", tfName(v.AttachTo))
			b.WriteString("}\n\n")
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}
