package catalog

import "fmt"

// LoadBalancerAddressReference binds DNS to the renderer's actual DO resource.
// Other provider address shapes require their own explicit output contract.
func LoadBalancerAddressReference(plan LoadBalancerPlan) (string, error) {
	if plan.Provider != ProviderDigitalOcean || plan.ResourceType != "digitalocean_loadbalancer" || plan.LBName == "" || plan.StableIP {
		return "", fmt.Errorf("load balancer address reference: unsupported provider/resource")
	}
	return "${digitalocean_loadbalancer." + tfName(plan.LBName) + ".ip}", nil
}
