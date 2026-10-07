package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/PyxCloud/terraform-provider-pyxcloud/pkg/engine"
)

// Consumers of the V1 topology must be able to construct every supported
// component payload through this public boundary, without internal imports.
var _ = []engine.AssembleComponent{
	{Cache: &engine.AssembleCache{}}, {DNS: &engine.AssembleDNS{}},
	{LB: &engine.AssembleLB{Listeners: []engine.AssembleLBListener{}}},
	{ObjectStorage: &engine.AssembleObjectStorage{}},
	{StaticSite: &engine.AssembleStaticSite{}},
	{BlockStorage: &engine.AssembleBlockStorage{}},
}

func TestPublicEngineRendersDigitalOceanWithoutInternalImports(t *testing.T) {
	catalog, err := engine.NewEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	in := engine.AssembleInput{Name: "specops-fixture", Provider: "digitalocean", Region: "Frankfurt", CIDR: "10.21.0.0/16", Subnets: []string{"10.21.1.0/24"}, ApplySecurityBaseline: true,
		Components: []engine.AssembleComponent{{Name: "app", Type: "virtual-machine", Count: 1, VM: &engine.AssembleVM{Architecture: "x86_64", CPU: "2", RAM: "4", OS: "ubuntu"}}}}
	docs, err := engine.AssembleHCL(context.Background(), catalog, in)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(docs, "\n")
	for _, want := range []string{`resource "digitalocean_droplet"`, `resource "digitalocean_vpc"`, `source = "digitalocean/digitalocean"`} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q", want)
		}
	}
	in.Components = []engine.AssembleComponent{{Name: "unknown", Type: "quantum-computer"}}
	if _, err := engine.AssembleHCL(context.Background(), catalog, in); err == nil {
		t.Fatal("unsupported component accepted")
	}
}
