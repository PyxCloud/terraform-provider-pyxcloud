package catalog

import (
	"strings"
	"testing"
)

// Regression for the single-VM (pyx_virtual_machine) DO render dropping tag +
// ssh_keys: before the fix, renderVMDO hardcoded `tags = ["pyxcloud"]` and never
// emitted ssh_keys, so a DO firewall/LB selecting the box by tag never matched and
// break-glass SSH keys never landed. Now Tag/SSHKeys thread through to the droplet.
func TestRenderVMHCL_DO_TagAndSSHKeys(t *testing.T) {
	plan := VMPlan{
		Provider:     ProviderDigitalOcean,
		CSPRegion:    "fra1",
		Image:        "ubuntu-24-04-x64",
		InstanceType: "s-1vcpu-1gb",
		Instances:    []VMInstancePlan{{Name: "vault-prod-1"}},
		NetworkName:  "prod-net",
		Tag:          "pyx-vault-prod",
		SSHKeys:      []string{"57496891"},
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{
		`tags = ["pyxcloud", "pyx-vault-prod"]`,
		`ssh_keys = ["57496891"]`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("rendered DO droplet missing %q\n---\n%s", want, hcl)
		}
	}
}

func TestRenderVMHCL_DO_DefaultsWhenUnset(t *testing.T) {
	plan := VMPlan{
		Provider:     ProviderDigitalOcean,
		CSPRegion:    "fra1",
		Image:        "ubuntu-24-04-x64",
		InstanceType: "s-1vcpu-1gb",
		Instances:    []VMInstancePlan{{Name: "web-1"}},
	}
	hcl, err := RenderVMHCL(plan)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(hcl, `tags = ["pyxcloud"]`) {
		t.Errorf("expected default pyxcloud-only tag:\n%s", hcl)
	}
	if strings.Contains(hcl, "ssh_keys") {
		t.Errorf("expected no ssh_keys line when none set:\n%s", hcl)
	}
}
