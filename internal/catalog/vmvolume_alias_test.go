package catalog

// vmvolume_alias_test.go — the vm-volume vocabulary (rename of block-storage,
// roadmap P6 delta-closure): `vm-volume` is the canonical token, `block-storage`
// and `volume` are accepted aliases resolving to the identical plan, and on
// providers without a native volume service the component routes through the
// STANDARD mitigation path (NFS self-host VM) like every other mitigated type.

import (
	"context"
	"strings"
	"testing"
)

func TestCanonicalVMVolumeType(t *testing.T) {
	for _, tok := range []string{"vm-volume", "block-storage", "volume", " VM-VOLUME "} {
		got, ok := CanonicalVMVolumeType(tok)
		if !ok || got != TypeVMVolume {
			t.Errorf("CanonicalVMVolumeType(%q) = %q, %v; want vm-volume, true", tok, got, ok)
		}
	}
	if _, ok := CanonicalVMVolumeType("object-storage"); ok {
		t.Error("object-storage must not resolve as vm-volume")
	}
}

func TestVMVolumeAliasPlansIdentical(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	for _, tok := range []string{"vm-volume", "block-storage", "volume"} {
		plan, err := TranslateVMVolume(context.Background(), cat, VMVolumeSpec{
			Name: "data", Region: "Dublin", Provider: "aws", SizeGB: 100, TargetVM: "app",
		})
		if err != nil {
			t.Fatalf("%s: TranslateVMVolume: %v", tok, err)
		}
		if plan.ResourceType != "aws_ebs_volume" || plan.SizeGB != 100 {
			t.Errorf("%s: plan = %+v", tok, plan)
		}
	}
}

func TestAssembleHCLVMVolumeCanonicalToken(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "aws", Region: "Dublin",
		Components: []AssembleComponent{
			{Name: "app", Type: "virtual-machine", Count: 1, VM: &AssembleVM{Architecture: ArchX8664, CPU: "2", RAM: "4", OS: OSUbuntu}},
			{Name: "data", Type: "vm-volume", VMVolume: &AssembleVMVolume{SizeGB: 100, TargetVM: "app"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL(vm-volume): %v", err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{"resource \"aws_ebs_volume\"", "resource \"aws_volume_attachment\""} {
		if !strings.Contains(all, want) {
			t.Errorf("assembled HCL missing %q\n---\n%s", want, all)
		}
	}
}

// The legacy tokens still assemble on a native provider (compat path).
func TestAssembleHCLVMVolumeLegacyAliasTokens(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	for _, tok := range []string{"block-storage", "volume"} {
		docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
			Name: "demo", Provider: "digitalocean", Region: "Amsterdam",
			Components: []AssembleComponent{
				{Name: "app", Type: "virtual-machine", Count: 1, VM: &AssembleVM{Architecture: ArchX8664, CPU: "2", RAM: "4", OS: OSUbuntu}},
				{Name: "data", Type: tok, VMVolume: &AssembleVMVolume{SizeGB: 50, TargetVM: "app"}},
			},
		})
		if err != nil {
			t.Fatalf("AssembleHCL(%s): %v", tok, err)
		}
		if !strings.Contains(strings.Join(docs, "\n"), "resource \"digitalocean_volume\"") {
			t.Errorf("alias %s: missing digitalocean_volume\n%s", tok, strings.Join(docs, "\n"))
		}
	}
}

// On a provider with NO native volume service (linode), vm-volume routes through
// the standard mitigation (NFS self-host VM) — same abstraction, no per-provider code.
func TestAssembleHCLVMVolumeMitigatedOnLinode(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatalf("embedded catalog: %v", err)
	}
	docs, err := AssembleHCL(context.Background(), cat, AssembleInput{
		Name: "demo", Provider: "linode", Region: "London",
		Components: []AssembleComponent{
			{Name: "app", Type: "virtual-machine", Count: 1, VM: &AssembleVM{Architecture: ArchX8664, CPU: "2", RAM: "4", OS: OSUbuntu}},
			{Name: "datavol", Type: "vm-volume", VMVolume: &AssembleVMVolume{SizeGB: 100, TargetVM: "app"}},
		},
	})
	if err != nil {
		t.Fatalf("AssembleHCL(linode vm-volume): %v", err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, "itsthenetwork/nfs-server-alpine") {
		t.Errorf("expected NFS self-host mitigation, got:\n%s", all)
	}
	if !strings.Contains(all, "resource \"linode_instance\"") {
		t.Errorf("expected linode VM mitigation host, got:\n%s", all)
	}
}

// The exported engine retained BlockStorage on main; merging the canonical
// vm-volume vocabulary must preserve that caller payload and its rendering.
func TestLegacyBlockStoragePayloadMatchesVMVolume(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	input := AssembleInput{Name: "demo", Provider: "aws", Region: "Dublin", Components: []AssembleComponent{
		{Name: "app", Type: "virtual-machine", Count: 1, VM: &AssembleVM{Architecture: ArchX8664, CPU: "2", RAM: "4", OS: OSUbuntu}},
		{Name: "data", Type: "block-storage", BlockStorage: &AssembleVMVolume{SizeGB: 100, TargetVM: "app"}},
	}}
	legacy, err := AssembleHCL(context.Background(), cat, input)
	if err != nil {
		t.Fatal(err)
	}
	input.Components[1].VMVolume = input.Components[1].BlockStorage
	input.Components[1].BlockStorage = nil
	input.Components[1].Type = "vm-volume"
	canonical, err := AssembleHCL(context.Background(), cat, input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(legacy, "\n") != strings.Join(canonical, "\n") {
		t.Fatal("legacy and canonical volume renders differ")
	}
}
