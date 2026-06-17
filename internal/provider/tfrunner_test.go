package provider

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTFRunnerWriteConfig(t *testing.T) {
	dir := t.TempDir()
	r := &tfRunner{workDir: dir, execPath: "terraform"}

	// Pre-existing stale .tf + a state file that must survive.
	if err := os.WriteFile(filepath.Join(dir, "old.tf"), []byte("# stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "terraform.tfstate"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	docs := []string{"resource \"aws_vpc\" \"v\" {}", "resource \"aws_subnet\" \"s\" {}"}
	if err := r.writeConfig(docs); err != nil {
		t.Fatalf("writeConfig: %v", err)
	}

	// The two generated docs exist.
	for i := range docs {
		name := filepath.Join(dir, "pyx_00"+string(rune('0'+i))+".tf")
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
		if string(b) != docs[i] {
			t.Errorf("%s = %q want %q", name, b, docs[i])
		}
	}
	// The stale .tf was removed.
	if _, err := os.Stat(filepath.Join(dir, "old.tf")); !os.IsNotExist(err) {
		t.Error("stale old.tf should have been removed")
	}
	// The state file survived (not a .tf).
	if _, err := os.Stat(filepath.Join(dir, "terraform.tfstate")); err != nil {
		t.Error("terraform.tfstate must survive a re-write")
	}
}

func TestNewTFRunnerMissingTerraform(t *testing.T) {
	// With an empty PATH, terraform won't be found → a clear error (not a panic).
	t.Setenv("PATH", "")
	_, err := newTFRunner(t.TempDir())
	if err == nil {
		t.Fatal("expected error when terraform is not on PATH")
	}
}
