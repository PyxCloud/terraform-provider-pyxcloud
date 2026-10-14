package catalog

import (
	"context"
	"strings"
	"testing"
)

func baselinePlainVMInput(name string) AssembleInput {
	return AssembleInput{Name: name, Provider: ProviderDigitalOcean, Region: "New York", CIDR: "10.22.1.0/24", Subnets: []string{"10.22.1.0/24"}, ApplySecurityBaseline: true, Components: []AssembleComponent{{Name: "app", Type: "virtual-machine", Count: 1, VM: &AssembleVM{Architecture: "x86_64", CPU: "1", RAM: "1", OS: "ubuntu", OSVersion: "22.04"}}}}
}
func TestBaselinePlainVMWithoutIngressHasScopedFirewall(t *testing.T) {
	cat, err := NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	input := baselinePlainVMInput("first-production")
	docs, err := AssembleHCL(context.Background(), cat, input)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(docs, "\n")
	if !strings.Contains(all, `resource "digitalocean_firewall"`) {
		t.Fatal("baseline no-ingress VM lacks firewall")
	}
	if !strings.Contains(all, `tags = ["pyx-first-production-app"]`) || !strings.Contains(all, `tags = ["pyxcloud", "pyx-first-production-app"]`) {
		t.Fatal("firewall and VM must share exact environment/component tag")
	}
	if strings.Contains(all, `tags = ["pyxcloud"]`) {
		t.Fatal("generic selector exposes another environment fleet")
	}
	if input.Components[0].VM.Tag != "" {
		t.Fatal("assembler mutated caller payload")
	}
	other, err := AssembleHCL(context.Background(), cat, baselinePlainVMInput("second-production"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(other, "\n"), "pyx-first-production-app") {
		t.Fatal("different environment inherited firewall selector")
	}
}
func TestBaselinePlainVMRefusesGenericExplicitSelector(t *testing.T) {
	cat, _ := NewEmbedded()
	input := baselinePlainVMInput("first-production")
	input.Components[0].VM.Tag = "pyxcloud"
	if _, err := AssembleHCL(context.Background(), cat, input); err == nil {
		t.Fatal("generic explicit fleet selector accepted")
	}
}
