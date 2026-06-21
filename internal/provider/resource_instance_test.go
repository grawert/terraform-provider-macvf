package provider

import (
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestEfiVarsPath_BootDiskPresent(t *testing.T) {
	disks := []instanceDiskAttachmentModel{
		{DiskID: types.StringValue("/pools/data/ubuntu-data.img"), IsBoot: types.BoolValue(false)},
		{DiskID: types.StringValue("/pools/data/ubuntu-root.img"), IsBoot: types.BoolValue(true)},
	}
	got, err := efiVarsPath("my-vm", disks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join("/pools/data", "my-vm.efivars.fd")
	if got != want {
		t.Errorf("efiVarsPath = %q, want %q", got, want)
	}
}

func TestEfiVarsPath_NoBootDisk(t *testing.T) {
	disks := []instanceDiskAttachmentModel{
		{DiskID: types.StringValue("/pools/data/ubuntu.img"), IsBoot: types.BoolValue(false)},
	}
	_, err := efiVarsPath("my-vm", disks)
	if err == nil {
		t.Error("expected error for no boot disk, got nil")
	}
}

func TestEfiVarsPath_EmptyDisks(t *testing.T) {
	_, err := efiVarsPath("my-vm", nil)
	if err == nil {
		t.Error("expected error for empty disk list, got nil")
	}
}
