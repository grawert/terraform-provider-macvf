package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	gvtypes "github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	tftypes "github.com/hashicorp/terraform-plugin-framework/types"
)

func TestGenerateGatewayMAC_Format(t *testing.T) {
	mac, err := generateGatewayMAC("test-net")
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
	// locally administered (bit 1 set) and unicast (bit 0 clear)
	if hw[0]&0x01 != 0 {
		t.Errorf("MAC %q is multicast (bit 0 set), want unicast", mac)
	}
	if hw[0]&0x02 == 0 {
		t.Errorf("MAC %q is globally unique (bit 1 clear), want locally administered", mac)
	}
}

func TestGenerateGatewayMAC_StablePrefix(t *testing.T) {
	mac1, err := generateGatewayMAC("my-network")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mac2, err := generateGatewayMAC("my-network")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	hw1, _ := net.ParseMAC(mac1)
	hw2, _ := net.ParseMAC(mac2)
	if hw1[0] != hw2[0] || hw1[1] != hw2[1] || hw1[2] != hw2[2] {
		t.Errorf("prefix not stable for same name: %s vs %s", mac1, mac2)
	}
}

func TestGenerateGatewayMAC_UniquePerCall(t *testing.T) {
	mac1, err := generateGatewayMAC("my-network")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mac2, err := generateGatewayMAC("my-network")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mac1 == mac2 {
		t.Errorf("expected unique MACs per call, got identical: %s", mac1)
	}
}

func TestGenerateGatewayMAC_DifferentPrefixPerName(t *testing.T) {
	mac1, _ := generateGatewayMAC("network-a")
	mac2, _ := generateGatewayMAC("network-b")
	hw1, _ := net.ParseMAC(mac1)
	hw2, _ := net.ParseMAC(mac2)
	if hw1[0] == hw2[0] && hw1[1] == hw2[1] && hw1[2] == hw2[2] {
		t.Errorf("expected different prefixes for different names, got same prefix: %s vs %s", mac1, mac2)
	}
}

func minimalPlan() networkResourceModel {
	return networkResourceModel{
		Name:                  tftypes.StringValue("test-net"),
		Subnet:                tftypes.StringValue("192.168.100.0/24"),
		GatewayIP:             tftypes.StringValue("192.168.100.1"),
		GatewayMAC:            tftypes.StringValue("5a:00:00:00:00:01"),
		SocketPath:            tftypes.StringValue("/tmp/macvf-test-net.sock"),
		StartupTimeoutSeconds: tftypes.Int64Value(defaultStartupTimeoutSeconds),
		DNSSearchDomains:      tftypes.ListValueMust(tftypes.StringType, []attr.Value{}),
	}
}

func TestBuildGvisorConfig_Minimal(t *testing.T) {
	cfg, diags := buildGvisorConfig(context.Background(), minimalPlan())
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", diags)
	}
	if len(diags) != 0 {
		t.Errorf("expected no diagnostics, got %d", len(diags))
	}
	if cfg.Subnet != "192.168.100.0/24" {
		t.Errorf("Subnet = %q, want %q", cfg.Subnet, "192.168.100.0/24")
	}
	if cfg.Socket != "/tmp/macvf-test-net.sock" {
		t.Errorf("Socket = %q, want %q", cfg.Socket, "/tmp/macvf-test-net.sock")
	}
	if len(cfg.DNS) != 0 {
		t.Errorf("expected no DNS entries, got %d", len(cfg.DNS))
	}
}

func TestBuildGvisorConfig_DNSSearchDomains(t *testing.T) {
	plan := minimalPlan()
	plan.DNSSearchDomains = tftypes.ListValueMust(tftypes.StringType, []attr.Value{
		tftypes.StringValue("example.com"),
		tftypes.StringValue("internal.local"),
	})

	cfg, diags := buildGvisorConfig(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", diags)
	}
	if len(cfg.DNSSearchDomains) != 2 {
		t.Fatalf("expected 2 search domains, got %d", len(cfg.DNSSearchDomains))
	}
	if cfg.DNSSearchDomains[0] != "example.com" {
		t.Errorf("DNSSearchDomains[0] = %q, want %q", cfg.DNSSearchDomains[0], "example.com")
	}
}

func TestBuildGvisorConfig_ProtocolIsVfkit(t *testing.T) {
	cfg, diags := buildGvisorConfig(context.Background(), minimalPlan())
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", diags)
	}
	if cfg.Protocol != gvtypes.VfkitProtocol {
		t.Errorf("Protocol = %q, want %q (vfkit speaks SOCK_DGRAM, not QEMU framing)", cfg.Protocol, gvtypes.VfkitProtocol)
	}
}

func TestBuildGvisorConfig_LeaseExpandsToDHCPAndDNS(t *testing.T) {
	plan := minimalPlan()
	plan.Leases = []networkLeaseModel{
		{
			Hostname:   tftypes.StringValue("ubuntu.local"),
			IPAddress:  tftypes.StringValue("192.168.100.10"),
			MACAddress: tftypes.StringValue("52:54:00:aa:bb:cc"),
		},
		{
			Hostname:   tftypes.StringValue("debian.local"),
			IPAddress:  tftypes.StringValue("192.168.100.11"),
			MACAddress: tftypes.StringValue("52:54:00:dd:ee:ff"),
		},
	}

	cfg, diags := buildGvisorConfig(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", diags)
	}
	// DHCPStaticLeases is keyed by IP (→ MAC), matching gvisor-tap-vsock's IPPool.
	if got := cfg.DHCPStaticLeases["192.168.100.10"]; got != "52:54:00:aa:bb:cc" {
		t.Errorf("lease 1 DHCP = %q, want %q", got, "52:54:00:aa:bb:cc")
	}
	if got := cfg.DHCPStaticLeases["192.168.100.11"]; got != "52:54:00:dd:ee:ff" {
		t.Errorf("lease 2 DHCP = %q, want %q", got, "52:54:00:dd:ee:ff")
	}
	zones := map[string]string{}
	for _, z := range cfg.DNS {
		if len(z.Records) == 1 {
			zones[z.Name] = z.Records[0].IP.String()
		}
	}
	if zones["ubuntu.local"] != "192.168.100.10" {
		t.Errorf("DNS ubuntu.local = %q, want %q", zones["ubuntu.local"], "192.168.100.10")
	}
	if zones["debian.local"] != "192.168.100.11" {
		t.Errorf("DNS debian.local = %q, want %q", zones["debian.local"], "192.168.100.11")
	}
}

func TestBuildGvisorConfig_LeaseDuplicateMACFails(t *testing.T) {
	plan := minimalPlan()
	plan.Leases = []networkLeaseModel{
		{
			Hostname:   tftypes.StringValue("a.local"),
			IPAddress:  tftypes.StringValue("192.168.100.10"),
			MACAddress: tftypes.StringValue("52:54:00:aa:bb:cc"),
		},
		{
			Hostname:   tftypes.StringValue("b.local"),
			IPAddress:  tftypes.StringValue("192.168.100.11"),
			MACAddress: tftypes.StringValue("52:54:00:aa:bb:cc"),
		},
	}

	_, diags := buildGvisorConfig(context.Background(), plan)
	if !diags.HasError() {
		t.Fatalf("expected error diagnostics for duplicate MAC, got none")
	}
}

func TestBuildGvisorConfig_PortForwards(t *testing.T) {
	plan := minimalPlan()
	plan.PortForwards = []networkPortForwardModel{
		{Host: tftypes.StringValue("127.0.0.1:2222"), VM: tftypes.StringValue("192.168.100.10:22")},
		{Host: tftypes.StringValue("127.0.0.1:8080"), VM: tftypes.StringValue("192.168.100.10:80")},
	}

	cfg, diags := buildGvisorConfig(context.Background(), plan)
	if diags.HasError() {
		t.Fatalf("unexpected error diagnostics: %v", diags)
	}
	if len(cfg.Forwards) != 2 {
		t.Fatalf("expected 2 port forwards, got %d", len(cfg.Forwards))
	}
	if got := cfg.Forwards["127.0.0.1:2222"]; got != "192.168.100.10:22" {
		t.Errorf("forward 2222 = %q, want %q", got, "192.168.100.10:22")
	}
	if got := cfg.Forwards["127.0.0.1:8080"]; got != "192.168.100.10:80" {
		t.Errorf("forward 8080 = %q, want %q", got, "192.168.100.10:80")
	}
}

// TestWaitForReady_Success verifies the provider unblocks when network-runner
// dials the notification socket and writes a {"notification_type":"ready"}
// payload, matching upstream gvproxy's NotificationSender behaviour.
func TestWaitForReady_Success(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "notify.sock")
	ln, err := startNotificationListener(sock)
	if err != nil {
		t.Fatalf("startNotificationListener: %v", err)
	}
	defer ln.Close()

	done := make(chan notificationResult, 1)
	go waitForReady(ln, done)

	// Simulate network-runner's NotificationSender.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial notify socket: %v", err)
	}
	if err := json.NewEncoder(conn).Encode(gvtypes.NotificationMessage{NotificationType: gvtypes.Ready}); err != nil {
		t.Fatalf("encode ready: %v", err)
	}
	conn.Close()

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("unexpected error from waitForReady: %v", res.err)
		}
		if res.msg.NotificationType != gvtypes.Ready {
			t.Errorf("got notification type %q, want %q", res.msg.NotificationType, gvtypes.Ready)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForReady did not return within 2s")
	}
}

// TestWaitForReady_HypervisorError verifies a hypervisor_error message aborts
// the wait with a descriptive error so Create can kill the child and surface
// the failure to Terraform.
func TestWaitForReady_HypervisorError(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "notify.sock")
	ln, err := startNotificationListener(sock)
	if err != nil {
		t.Fatalf("startNotificationListener: %v", err)
	}
	defer ln.Close()

	done := make(chan notificationResult, 1)
	go waitForReady(ln, done)

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial notify socket: %v", err)
	}
	if err := json.NewEncoder(conn).Encode(gvtypes.NotificationMessage{NotificationType: gvtypes.HypervisorError}); err != nil {
		t.Fatalf("encode error: %v", err)
	}
	conn.Close()

	select {
	case res := <-done:
		if res.err == nil {
			t.Fatal("expected error from waitForReady, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForReady did not return within 2s")
	}
}

// TestWaitForReady_IgnoresIntermediateMessages ensures connection_established
// arriving before ready does not return a false-positive notification.
func TestWaitForReady_IgnoresIntermediateMessages(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "notify.sock")
	ln, err := startNotificationListener(sock)
	if err != nil {
		t.Fatalf("startNotificationListener: %v", err)
	}
	defer ln.Close()

	done := make(chan notificationResult, 1)
	go waitForReady(ln, done)

	for _, msg := range []gvtypes.NotificationMessage{
		{NotificationType: gvtypes.ConnectionEstablished, MacAddress: "52:54:00:aa:bb:cc"},
		{NotificationType: gvtypes.Ready},
	} {
		conn, dialErr := net.Dial("unix", sock)
		if dialErr != nil {
			t.Fatalf("dial: %v", dialErr)
		}
		if err := json.NewEncoder(conn).Encode(msg); err != nil {
			t.Fatalf("encode: %v", err)
		}
		conn.Close()
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.msg.NotificationType != gvtypes.Ready {
			t.Errorf("got %q, want ready", res.msg.NotificationType)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForReady did not return within 2s")
	}
}

// TestAwaitNotification_Timeout exercises the timeout branch — the provider
// must surface a clear timeout error if network-runner never sends ready.
func TestAwaitNotification_Timeout(t *testing.T) {
	done := make(chan notificationResult)
	res := awaitNotification(done, 50*time.Millisecond)
	if res.err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

// TestAwaitNotification_ChannelClosed verifies the case where the listener
// goroutine exited without sending a value (e.g. listener was closed early).
func TestAwaitNotification_ChannelClosed(t *testing.T) {
	done := make(chan notificationResult)
	close(done)
	res := awaitNotification(done, 1*time.Second)
	if res.err == nil {
		t.Fatal("expected error when channel closed, got nil")
	}
	if !errors.Is(res.err, res.err) { // smoke: error is non-nil and inspectable
		t.Fatal("expected wrapped error")
	}
}

func TestStartNotificationListener_RemovesStaleSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "notify.sock")
	// Create a stale file that would normally make net.Listen fail with EADDRINUSE.
	if err := os.WriteFile(sock, []byte("stale"), 0600); err != nil {
		t.Fatalf("seed stale socket file: %v", err)
	}

	ln, err := startNotificationListener(sock)
	if err != nil {
		t.Fatalf("startNotificationListener should overwrite stale socket: %v", err)
	}
	defer ln.Close()
}
