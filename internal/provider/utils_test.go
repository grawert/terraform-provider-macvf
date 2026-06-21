package provider

import (
	"net"
	"testing"
)

func TestGenerateLocalMAC_Random_Format(t *testing.T) {
	mac, err := generateLocalMAC(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hw, err := net.ParseMAC(mac)
	if err != nil {
		t.Fatalf("generated MAC %q is not valid: %v", mac, err)
	}
	if len(hw) != 6 {
		t.Fatalf("expected 6-byte MAC, got %d bytes", len(hw))
	}
	if hw[0]&0x01 != 0 {
		t.Errorf("MAC %q is multicast (bit 0 set), want unicast", mac)
	}
	if hw[0]&0x02 == 0 {
		t.Errorf("MAC %q is globally unique (bit 1 clear), want locally administered", mac)
	}
}

func TestGenerateLocalMAC_Random_UniquePerCall(t *testing.T) {
	mac1, err := generateLocalMAC(nil)
	if err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}
	mac2, err := generateLocalMAC(nil)
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if mac1 == mac2 {
		t.Errorf("expected unique MACs per call, got identical: %s", mac1)
	}
}

func TestGenerateLocalMAC_Prefix_UsesPrefix(t *testing.T) {
	prefix := []byte{0x10, 0x20, 0x30}
	mac, err := generateLocalMAC(prefix)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hw, err := net.ParseMAC(mac)
	if err != nil {
		t.Fatalf("generated MAC %q is not valid: %v", mac, err)
	}
	// First byte has prefix[0] with LA bit set and multicast bit cleared.
	wantByte0 := (prefix[0] &^ macMulticastBit) | macLocallyAdministered
	if hw[0] != wantByte0 {
		t.Errorf("byte 0 = %02x, want %02x", hw[0], wantByte0)
	}
	if hw[1] != prefix[1] {
		t.Errorf("byte 1 = %02x, want %02x", hw[1], prefix[1])
	}
	if hw[2] != prefix[2] {
		t.Errorf("byte 2 = %02x, want %02x", hw[2], prefix[2])
	}
}
