package catalog

import (
	"strings"
	"testing"
)

func TestLoadBalancerAddressReferenceUsesRendererLabel(t *testing.T) {
	p := LoadBalancerPlan{Provider: ProviderDigitalOcean, ResourceType: "digitalocean_loadbalancer", LBName: "edge-prod"}
	got, err := LoadBalancerAddressReference(p)
	if err != nil || got != "${digitalocean_loadbalancer.edge-prod.ip}" {
		t.Fatalf("got %q %v", got, err)
	}
	p.Provider = ProviderAWS
	if _, err := LoadBalancerAddressReference(p); err == nil {
		t.Fatal("unsupported provider accepted")
	}
}
func TestDOFirewallSharedFleetTagRendersOnce(t *testing.T) {
	got := renderSGDO(SecurityGroupPlan{SGName: "baseline", DropletTags: []string{"fleet", "fleet"}})
	if strings.Count(got, "resource \"digitalocean_firewall\"") != 1 {
		t.Fatalf("duplicate fleet firewalls: %s", got)
	}
}
