package catalog

import "fmt"

// VMAddressReference returns the Terraform interpolation for an instance in
// the canonical translated plan. Its label uses the renderer's own naming
// function. Unsupported providers are refused rather than guessed.
func VMAddressReference(plan VMPlan, instance int) (string, error) {
	if plan.Provider != ProviderDigitalOcean || plan.ResourceType != "digitalocean_droplet" {
		return "", fmt.Errorf("vm address reference: unsupported provider/resource")
	}
	if instance < 0 || instance >= len(plan.Instances) || plan.Instances[instance].Name == "" {
		return "", fmt.Errorf("vm address reference: unknown instance")
	}
	return "${digitalocean_droplet." + tfName(plan.Instances[instance].Name) + ".ipv4_address}", nil
}
