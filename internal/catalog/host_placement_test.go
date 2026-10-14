package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestExplicitServiceHostAttachesSealedBootstrapWithoutExtraVM(t *testing.T) {
	in := AssembleInput{Name: "host-test", Provider: "digitalocean", Region: "Frankfurt", CIDR: "10.3.1.0/24", Subnets: []string{"10.3.1.0/24"}, Components: []AssembleComponent{
		{Name: "app", Type: "virtual-machine", Count: 1, VM: &AssembleVM{Architecture: "x86_64", CPU: "1", RAM: "2", OS: "ubuntu", OSVersion: "22.04"}},
		{Name: "db", Type: "managed-database", Placement: "vm", HostVM: "app", HostBootstrap: "#!/bin/bash\nset -e\necho sealed-service-recipe\n"}}}
	docs, e := AssembleHCL(context.Background(), MustEmbedded(), in)
	if e != nil {
		t.Fatal(e)
	}
	all := strings.Join(docs, "\n")
	if strings.Count(all, "resource \"digitalocean_droplet\"") != 1 || !strings.Contains(all, "sealed-service-recipe") {
		t.Fatalf("host association lost %s", all)
	}
	if in.Components[0].VM.UserData != "" {
		t.Fatal("caller host mutated")
	}
	in.Components[1].HostVM = "missing"
	if _, e := AssembleHCL(context.Background(), MustEmbedded(), in); e == nil {
		t.Fatal("missing host accepted")
	}
	in.Components[1].HostVM = "app"
	in.Components[1].HostBootstrap = ""
	if _, e := AssembleHCL(context.Background(), MustEmbedded(), in); e == nil {
		t.Fatal("missing bootstrap accepted")
	}
}
