package catalog

import (
	"fmt"
	"strings"
)

// RenderKeyValueStoreHCL renders a KeyValueStorePlan into provider HCL.
// AWS -> aws_dynamodb_table (on-demand, encrypted, PITR on); DigitalOcean ->
// Managed Redis (digitalocean_database_cluster engine=redis), private to the
// place's VPC. Any other provider never reaches here (TranslateKeyValueStore
// rejects it with a clean ErrComponentUnsupported).
func RenderKeyValueStoreHCL(plan KeyValueStorePlan) (string, error) {
	switch plan.Provider {
	case ProviderAWS:
		return renderKeyValueStoreAWS(plan), nil
	case ProviderDigitalOcean:
		return renderKeyValueStoreDO(plan), nil
	default:
		return "", fmt.Errorf("render: unsupported provider %q for key-value-store", plan.Provider)
	}
}

func renderKeyValueStoreAWS(p KeyValueStorePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_dynamodb_table\" %q {\n", name)
	fmt.Fprintf(&b, "  name         = %q\n", p.Name)
	// PAY_PER_REQUEST = on-demand: no capacity planning, scales to zero cost.
	b.WriteString("  billing_mode = \"PAY_PER_REQUEST\"\n")
	fmt.Fprintf(&b, "  hash_key     = %q\n", p.PartitionKey)
	b.WriteString("  attribute {\n")
	fmt.Fprintf(&b, "    name = %q\n", p.PartitionKey)
	b.WriteString("    type = \"S\"\n")
	b.WriteString("  }\n")
	// SECURE + DURABLE BY DEFAULT: server-side encryption + point-in-time recovery.
	b.WriteString("  server_side_encryption {\n    enabled = true\n  }\n")
	b.WriteString("  point_in_time_recovery {\n    enabled = true\n  }\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", p.Name)
	b.WriteString("}\n")
	return b.String()
}

func renderKeyValueStoreDO(p KeyValueStorePlan) string {
	name := tfName(p.Name)
	var b strings.Builder
	// DO Managed Redis as the managed KV store. Private to the place's VPC.
	fmt.Fprintf(&b, "resource \"digitalocean_database_cluster\" %q {\n", name)
	fmt.Fprintf(&b, "  name       = %q\n", name)
	b.WriteString("  engine     = \"redis\"\n")
	ver := p.Version
	if ver == "" {
		ver = "7"
	}
	fmt.Fprintf(&b, "  version    = %q\n", ver)
	fmt.Fprintf(&b, "  size       = %q\n", p.NodeClass)
	fmt.Fprintf(&b, "  region     = %q\n", p.CSPRegion)
	if p.HA {
		b.WriteString("  node_count = 2\n")
	} else {
		b.WriteString("  node_count = 1\n")
	}
	if p.NetworkName != "" {
		fmt.Fprintf(&b, "  private_network_uuid = digitalocean_vpc.%s.id\n", tfName(p.NetworkName))
	}
	fmt.Fprintf(&b, "  tags = [\"pyxcloud\"]\n")
	b.WriteString("}\n")
	return b.String()
}
