package catalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestTranslateKeyValueStoreAWS asserts the DynamoDB plan + secure render.
func TestTranslateKeyValueStoreAWS(t *testing.T) {
	t.Parallel()
	plan, err := TranslateKeyValueStore(context.Background(), MustEmbedded(), KeyValueStoreSpec{
		Name: "jit-allowlist", Region: "Frankfurt", Provider: "aws", PartitionKey: "user_id",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "eu-central-1" {
		t.Errorf("csp_region = %q, want eu-central-1", plan.CSPRegion)
	}
	if plan.ResourceType != "aws_dynamodb_table" {
		t.Errorf("resource_type = %q, want aws_dynamodb_table", plan.ResourceType)
	}
	if plan.PartitionKey != "user_id" {
		t.Errorf("partition_key = %q, want user_id", plan.PartitionKey)
	}
	hcl, err := RenderKeyValueStoreHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "aws_dynamodb_table"`,
		`billing_mode = "PAY_PER_REQUEST"`,
		`hash_key     = "user_id"`,
		"server_side_encryption {",
		"point_in_time_recovery {",
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("AWS HCL missing %q\n%s", want, hcl)
		}
	}
}

// TestKeyValueStoreAWSDefaultPartitionKey asserts the default key is "id".
func TestKeyValueStoreAWSDefaultPartitionKey(t *testing.T) {
	t.Parallel()
	plan, err := TranslateKeyValueStore(context.Background(), MustEmbedded(), KeyValueStoreSpec{
		Name: "kv", Region: "Frankfurt", Provider: "aws",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.PartitionKey != "id" {
		t.Errorf("default partition_key = %q, want id", plan.PartitionKey)
	}
}

// TestTranslateKeyValueStoreDO asserts the Managed Redis (KV) plan + render, with
// the node class reusing the shared cache ladder and a private VPC wiring.
func TestTranslateKeyValueStoreDO(t *testing.T) {
	t.Parallel()
	plan, err := TranslateKeyValueStore(context.Background(), MustEmbedded(), KeyValueStoreSpec{
		Name: "jit-allowlist", Region: "Frankfurt", Provider: "digitalocean",
		MemoryGB: 2, HA: true, Network: "demo-net",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.CSPRegion != "fra1" {
		t.Errorf("csp_region = %q, want fra1", plan.CSPRegion)
	}
	if plan.ResourceType != "digitalocean_database_cluster" {
		t.Errorf("resource_type = %q, want digitalocean_database_cluster", plan.ResourceType)
	}
	// 2 GiB -> shared cache ladder db-s-1vcpu-2gb.
	if plan.NodeClass != "db-s-1vcpu-2gb" {
		t.Errorf("node_class = %q, want db-s-1vcpu-2gb (shared cache ladder)", plan.NodeClass)
	}
	hcl, err := RenderKeyValueStoreHCL(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`resource "digitalocean_database_cluster"`,
		`engine     = "redis"`,
		`size       = "db-s-1vcpu-2gb"`,
		`region     = "fra1"`,
		"node_count = 2",
		`private_network_uuid = digitalocean_vpc.demo-net.id`,
	} {
		if !strings.Contains(hcl, want) {
			t.Errorf("DO HCL missing %q\n%s", want, hcl)
		}
	}
}

// TestKeyValueStoreUnsupportedProvider asserts a clean error for a provider with
// no managed KV primitive in this component.
func TestKeyValueStoreUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := TranslateKeyValueStore(context.Background(), MustEmbedded(), KeyValueStoreSpec{
		Name: "kv", Region: "Frankfurt", Provider: "gcp",
	})
	var unsup ErrComponentUnsupported
	if !errors.As(err, &unsup) {
		t.Fatalf("expected ErrComponentUnsupported, got %v", err)
	}
	if unsup.Component != TypeKeyValueStore {
		t.Errorf("component = %q, want %q", unsup.Component, TypeKeyValueStore)
	}
}

func TestCanonicalKeyValueStoreType(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"key-value-store", "kv-store", "keyvalue-store", "dynamodb", "DynamoDB"} {
		if got, ok := CanonicalKeyValueStoreType(in); !ok || got != TypeKeyValueStore {
			t.Errorf("CanonicalKeyValueStoreType(%q) = %q,%v", in, got, ok)
		}
	}
	if _, ok := CanonicalKeyValueStoreType("cache"); ok {
		t.Error("cache should not be a key-value-store type")
	}
}
