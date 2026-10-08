package catalog

import (
	"fmt"
	"strings"
)

// bindServiceHosts consumes only an explicitly supplied sealed bash bootstrap.
// It never synthesizes a service recipe, secret, storage or another VM.
func bindServiceHosts(in AssembleInput) (AssembleInput, error) {
	components := append([]AssembleComponent(nil), in.Components...)
	hosts := map[string]int{}
	for i, c := range components {
		if c.Type == "virtual-machine" {
			if _, dup := hosts[c.Name]; dup {
				return in, fmt.Errorf("duplicate host %q", c.Name)
			}
			hosts[c.Name] = i
		}
	}
	for _, c := range components {
		if c.HostVM == "" {
			if c.HostBootstrap != "" {
				return in, fmt.Errorf("service %q: bootstrap without host", c.Name)
			}
			continue
		}
		i, ok := hosts[c.HostVM]
		if !ok || components[i].VM == nil || components[i].Count != 1 || c.Placement != "vm" || !ExplicitVMPlacement(c.Type) || !strings.HasPrefix(c.HostBootstrap, "#!/bin/bash\n") {
			return in, fmt.Errorf("service %q: exact single host and sealed bash bootstrap required", c.Name)
		}
		vm := *components[i].VM
		if vm.UserData != "" && !strings.HasPrefix(vm.UserData, "#!/bin/bash\n") {
			return in, fmt.Errorf("service %q: incompatible host bootstrap", c.Name)
		}
		vm.UserData += "\n" + c.HostBootstrap
		vm.UserData = strings.TrimPrefix(vm.UserData, "\n")
		components[i].VM = &vm
	}
	in.Components = components
	return in, nil
}
