package recipe

import "testing"

// A mount without a container path goes to /workspace (HOST alone).
func TestMountWithoutTargetDefaultsToWorkspace(t *testing.T) {
	r, err := Parse("name: x\nstack: go\nmounts:\n  - ~/proj\n  - /data:/data:ro\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Mounts) != 2 || r.Mounts[0] != "~/proj:/workspace" || r.Mounts[1] != "/data:/data:ro" {
		t.Fatalf("mounts: %q", r.Mounts)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}
