package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	gvtypes "github.com/containers/gvisor-tap-vsock/pkg/types"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	tftypes "github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource              = &networkResource{}
	_ resource.ResourceWithConfigure = &networkResource{}
)

type networkResource struct {
	providerName      string
	networkRunnerPath string
}

func NewNetworkResource() resource.Resource {
	return &networkResource{}
}

type networkResourceModel struct {
	ID                    tftypes.String            `tfsdk:"id"`
	Name                  tftypes.String            `tfsdk:"name"`
	Subnet                tftypes.String            `tfsdk:"subnet"`
	GatewayIP             tftypes.String            `tfsdk:"gateway_ip"`
	GatewayMAC            tftypes.String            `tfsdk:"gateway_mac"`
	SocketPath            tftypes.String            `tfsdk:"socket_path"`
	LogPath               tftypes.String            `tfsdk:"log_path"`
	StartupTimeoutSeconds tftypes.Int64             `tfsdk:"startup_timeout_seconds"`
	PID                   tftypes.Int64             `tfsdk:"pid"`
	DNSSearchDomains      tftypes.List              `tfsdk:"dns_search_domains"`
	Leases                []networkLeaseModel       `tfsdk:"lease"`
	PortForwards          []networkPortForwardModel `tfsdk:"port_forward"`
}

type networkPortForwardModel struct {
	Host tftypes.String `tfsdk:"host"`
	VM   tftypes.String `tfsdk:"vm"`
}

// networkLeaseModel co-locates the MAC, IP, and DNS hostname for one VM-side
// network identity. The provider expands every lease entry into both a
// DHCPStaticLeases (MAC → IP) and a DNSHosts (name → IP) record before
// handing the configuration to network-runner.
type networkLeaseModel struct {
	Hostname   tftypes.String `tfsdk:"hostname"`
	IPAddress  tftypes.String `tfsdk:"ip_address"`
	MACAddress tftypes.String `tfsdk:"mac_address"`
}

const (
	minStartupTimeoutSeconds     = 1
	maxStartupTimeoutSeconds     = 600
	defaultStartupTimeoutSeconds = 10
)

type startupTimeoutValidator struct{}

func (v startupTimeoutValidator) Description(_ context.Context) string {
	return fmt.Sprintf("Value must be between %d and %d (seconds).", minStartupTimeoutSeconds, maxStartupTimeoutSeconds)
}

func (v startupTimeoutValidator) MarkdownDescription(_ context.Context) string {
	return fmt.Sprintf("Value must be between `%d` and `%d` (seconds).", minStartupTimeoutSeconds, maxStartupTimeoutSeconds)
}

func (v startupTimeoutValidator) ValidateInt64(_ context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	val := req.ConfigValue.ValueInt64()
	if val < minStartupTimeoutSeconds || val > maxStartupTimeoutSeconds {
		message := fmt.Sprintf("startup_timeout_seconds must be between %d and %d (got %d).", minStartupTimeoutSeconds, maxStartupTimeoutSeconds, val)
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", message)
	}
}

func (r *networkResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		message := fmt.Sprintf("Expected *providerData, got %T", req.ProviderData)
		resp.Diagnostics.AddError("Unexpected provider data type", message)
		return
	}
	r.providerName = data.ProviderName
	r.networkRunnerPath = data.NetworkRunnerPath
}

func (r *networkResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (r *networkResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A virtual network for MacVF instances.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique ID of the network, typically the socket path.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the network.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"subnet": schema.StringAttribute{
				Description: "The subnet in CIDR notation.",
				Required:    true,
				Validators:  []validator.String{cidrValidator{}},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"gateway_ip": schema.StringAttribute{
				Description: "IP address of the virtual gateway. Defaults to the first host address in the subnet " +
					"(e.g. 192.168.100.1 for 192.168.100.0/24). Override to use a different address within the subnet.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"gateway_mac": schema.StringAttribute{
				Description: "MAC address of the virtual gateway interface. Auto-generated from the network name " +
					"and a random suffix to avoid collisions across networks and projects. Can be set explicitly " +
					"to guarantee uniqueness in multi-project setups.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"socket_path": schema.StringAttribute{
				Description: "Path to the Unix domain socket used by network-runner. " +
					"Defaults to /tmp/<provider>-<name>-<rand>.sock (random suffix ensures uniqueness across " +
					"projects using the same network name). Override when the default path is unsuitable " +
					"(e.g. path length limits or permissions).",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"log_path": schema.StringAttribute{
				Description: "Path to a file where network-runner stdout and stderr are written. " +
					"When not set, network-runner output is discarded.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"startup_timeout_seconds": schema.Int64Attribute{
				Description: fmt.Sprintf(
					"Maximum number of seconds to wait for network-runner to emit a 'ready' notification "+
						"before treating the Create as failed. Defaults to %d. Must be between %d and %d.",
					defaultStartupTimeoutSeconds, minStartupTimeoutSeconds, maxStartupTimeoutSeconds,
				),
				Optional:   true,
				Computed:   true,
				Validators: []validator.Int64{startupTimeoutValidator{}},
			},
			"pid": schema.Int64Attribute{
				Description: "PID of the running network-runner process.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"dns_search_domains": schema.ListAttribute{
				Description: "Search domains pushed to VMs via DHCP replies, allowing short hostname resolution.",
				ElementType: tftypes.StringType,
				Optional:    true,
			},
		},
		Blocks: map[string]schema.Block{
			"lease": schema.ListNestedBlock{
				Description: "Reserved network identity for a single VM. Each lease co-locates the MAC address, " +
					"IP address, and DNS hostname so they stay in sync without ad-hoc locals blocks. " +
					"The provider materializes each lease as a DHCP static lease and a DNS A-record " +
					"on the embedded gateway.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"hostname": schema.StringAttribute{
							Description: "DNS hostname served by the embedded gateway as an A-record pointing at ip_address.",
							Required:    true,
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.RequiresReplace(),
							},
						},
						"ip_address": schema.StringAttribute{
							Description: "IPv4 address assigned to the MAC via DHCP static lease " +
								"and used as the DNS A-record target. Must fall inside the network subnet.",
							Required: true,
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.RequiresReplace(),
							},
						},
						"mac_address": schema.StringAttribute{
							Description: "Locally administered unicast MAC address keyed in DHCP static leases. " +
								"Auto-generated if not set; stored in state so it remains stable across plans. " +
								"Use the same value in the instance's network_interface.mac_address.",
							Optional: true,
							Computed: true,
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.RequiresReplace(),
								stringplanmodifier.UseStateForUnknown(),
							},
						},
					},
				},
			},
			"port_forward": schema.ListNestedBlock{
				Description: "Forward a TCP port from the host into the virtual network, " +
					"enabling host-to-VM connectivity. For example, forwarding 127.0.0.1:2222 to " +
					"192.168.100.10:22 lets you reach the VM via `ssh -p 2222 admin@127.0.0.1`.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"host": schema.StringAttribute{
							Description: "Host-side address to listen on, in host:port format (e.g. \"127.0.0.1:2222\").",
							Required:    true,
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.RequiresReplace(),
							},
						},
						"vm": schema.StringAttribute{
							Description: "VM-side address to forward to, in ip:port format (e.g. \"192.168.100.10:22\").",
							Required:    true,
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.RequiresReplace(),
							},
						},
					},
				},
			},
		},
	}
}

func (r *networkResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan networkResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "creating network", map[string]any{
		"name":   plan.Name.ValueString(),
		"subnet": plan.Subnet.ValueString(),
	})

	if r.networkRunnerPath == "" {
		message := "network-runner is not available. " +
			"Ensure the provider was installed via `terraform init` or built with `make build`."
		resp.Diagnostics.AddError("network-runner not found", message)
		return
	}

	if plan.GatewayIP.IsNull() || plan.GatewayIP.IsUnknown() {
		_, ipNet, err := net.ParseCIDR(plan.Subnet.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Invalid subnet", err.Error())
			return
		}
		gw := make(net.IP, len(ipNet.IP))
		copy(gw, ipNet.IP)
		gw[len(gw)-1]++
		plan.GatewayIP = tftypes.StringValue(gw.String())
	}

	if plan.GatewayMAC.IsNull() || plan.GatewayMAC.IsUnknown() {
		mac, err := generateGatewayMAC(plan.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Failed to generate gateway MAC address", err.Error())
			return
		}
		plan.GatewayMAC = tftypes.StringValue(mac)
	}

	if plan.SocketPath.IsNull() || plan.SocketPath.IsUnknown() {
		socketPath, err := networkSocketPath(r.providerName, plan.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Failed to generate socket path", err.Error())
			return
		}
		plan.SocketPath = tftypes.StringValue(socketPath)
	}

	if plan.StartupTimeoutSeconds.IsNull() || plan.StartupTimeoutSeconds.IsUnknown() {
		plan.StartupTimeoutSeconds = tftypes.Int64Value(defaultStartupTimeoutSeconds)
	}

	for i := range plan.Leases {
		if plan.Leases[i].MACAddress.IsNull() || plan.Leases[i].MACAddress.IsUnknown() {
			mac, err := generateLocalMAC(nil)
			if err != nil {
				resp.Diagnostics.AddError("Failed to generate MAC address for lease", err.Error())
				return
			}
			plan.Leases[i].MACAddress = tftypes.StringValue(mac)
		}
	}

	config, diags := buildGvisorConfig(ctx, plan)
	if diags.HasError() {
		resp.Diagnostics.Append(diags...)
		return
	}

	// Generate an ephemeral notification socket path in /tmp/.
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		resp.Diagnostics.AddError("Failed to generate notification socket nonce", err.Error())
		return
	}
	notifyPath := fmt.Sprintf("/tmp/macvf-notify-%s.sock", hex.EncodeToString(nonce[:]))
	config.NotificationSocket = notifyPath

	configJSON, err := json.Marshal(config)
	if err != nil {
		resp.Diagnostics.AddError("Failed to marshal network config", err.Error())
		return
	}

	timeout := time.Duration(plan.StartupTimeoutSeconds.ValueInt64()) * time.Second

	notifyListener, err := startNotificationListener(notifyPath)
	if err != nil {
		resp.Diagnostics.AddError("Failed to listen on notification socket", err.Error())
		return
	}
	notifyDone := make(chan notificationResult, 1)
	go waitForReady(notifyListener, notifyDone)

	var logPath string
	var logFile *os.File
	if !plan.LogPath.IsNull() && !plan.LogPath.IsUnknown() {
		logPath = plan.LogPath.ValueString()
		logFile, err = os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			resp.Diagnostics.AddError("Failed to open network-runner log file", err.Error())
			return
		}
	} else {
		logFile, err = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err != nil {
			resp.Diagnostics.AddError("Failed to open /dev/null for network-runner output", err.Error())
			return
		}
	}

	cmd := exec.Command(r.networkRunnerPath, "--notification", notifyPath, "-")
	cmd.Stdin = bytes.NewReader(configJSON)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = notifyListener.Close()
		_ = os.Remove(notifyPath)
		resp.Diagnostics.AddError("Failed to start network-runner process", err.Error())
		return
	}

	result := awaitNotification(notifyDone, timeout)
	_ = notifyListener.Close()
	_ = os.Remove(notifyPath)

	if result.err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		detail := result.err.Error()
		if logPath != "" {
			detail += fmt.Sprintf("\n\nSee log for details: %s", logPath)
		}
		resp.Diagnostics.AddError("network-runner failed to become ready", detail)
		return
	}

	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		_ = logFile.Close()
		resp.Diagnostics.AddError("Failed to detach network-runner process", err.Error())
		return
	}

	_ = logFile.Close()
	tflog.Info(ctx, "network-runner log", map[string]any{"path": logPath})

	plan.ID = tftypes.StringValue(config.Socket)
	plan.PID = tftypes.Int64Value(int64(pid))
	tflog.Info(ctx, "network ready", map[string]any{
		"id":          plan.ID.ValueString(),
		"pid":         plan.PID.ValueInt64(),
		"gateway_ip":  plan.GatewayIP.ValueString(),
		"socket_path": plan.SocketPath.ValueString(),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *networkResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state networkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "reading network", map[string]any{
		"id":  state.ID.ValueString(),
		"pid": state.PID.ValueInt64(),
	})

	if !isProcessAlive(int(state.PID.ValueInt64())) {
		tflog.Warn(ctx, "network process not found, removing from state", map[string]any{
			"id":  state.ID.ValueString(),
			"pid": state.PID.ValueInt64(),
		})
		if err := os.Remove(state.SocketPath.ValueString()); err != nil && !os.IsNotExist(err) {
			tflog.Warn(ctx, "failed to remove stale network socket", map[string]any{
				"path":  state.SocketPath.ValueString(),
				"error": err.Error(),
			})
		}
		message := fmt.Sprintf("The network-runner process with PID %d is gone.", state.PID.ValueInt64())
		resp.Diagnostics.AddWarning("Network process not found", message)
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *networkResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	message := "Network resources cannot be updated. " +
		"Please destroy and recreate the resource with the new configuration."
	resp.Diagnostics.AddError("Updates not supported", message)
}

func (r *networkResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state networkResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "deleting network", map[string]any{
		"id":  state.ID.ValueString(),
		"pid": state.PID.ValueInt64(),
	})

	if err := terminateProcess(int(state.PID.ValueInt64()), 5*time.Second); err != nil {
		resp.Diagnostics.AddWarning("Failed to stop network-runner process",
			fmt.Sprintf("Could not send SIGTERM to PID %d: %s", state.PID.ValueInt64(), err))
	}

	if err := os.Remove(state.SocketPath.ValueString()); err != nil && !os.IsNotExist(err) {
		resp.Diagnostics.AddWarning("Failed to remove network socket file", err.Error())
	}

	if !state.LogPath.IsNull() && !state.LogPath.IsUnknown() {
		if err := os.Remove(state.LogPath.ValueString()); err != nil && !os.IsNotExist(err) {
			resp.Diagnostics.AddWarning("Failed to remove network-runner log file", err.Error())
		}
	}
}

type notificationResult struct {
	msg gvtypes.NotificationMessage
	err error
}

// startNotificationListener prepares an in-process unix-stream listener for
// the gvisor-tap-vsock NotificationSender. network-runner dials this socket
// and sends {"notification_type":"ready"} once the network stack is fully
// initialised — not just when the process started — giving a stronger
// readiness guarantee than polling the gvisor socket for connectivity.
// The listener lives only for the duration of Create; it is closed and the
// socket file removed once the notification arrives.
func startNotificationListener(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("create notification socket directory: %w", err)
	}
	_ = os.Remove(path)
	return net.Listen("unix", path)
}

// waitForReady consumes notification messages from network-runner until it
// sees `ready` (success), `hypervisor_error` (failure), or the listener is
// closed. Multiple short-lived connections are expected because the upstream
// NotificationSender dials a fresh socket per message.
func waitForReady(ln net.Listener, done chan<- notificationResult) {
	defer close(done)
	for {
		conn, err := ln.Accept()
		if err != nil {
			done <- notificationResult{err: err}
			return
		}
		msg, decodeErr := decodeNotification(conn)
		_ = conn.Close()
		if decodeErr != nil {
			continue
		}
		switch msg.NotificationType {
		case gvtypes.Ready:
			done <- notificationResult{msg: msg}
			return
		case gvtypes.HypervisorError:
			done <- notificationResult{msg: msg, err: errors.New("network-runner reported hypervisor_error")}
			return
		}
	}
}

func decodeNotification(r io.Reader) (gvtypes.NotificationMessage, error) {
	var msg gvtypes.NotificationMessage
	if err := json.NewDecoder(r).Decode(&msg); err != nil {
		return gvtypes.NotificationMessage{}, err
	}
	return msg, nil
}

func awaitNotification(done <-chan notificationResult, timeout time.Duration) notificationResult {
	select {
	case res, ok := <-done:
		if !ok {
			return notificationResult{err: errors.New("notification listener closed before any message arrived")}
		}
		return res
	case <-time.After(timeout):
		err := fmt.Errorf("timed out after %s waiting for network-runner 'ready' notification", timeout)
		return notificationResult{err: err}
	}
}

// networkRunnerConfig mirrors the runnerConfig in cmd/network-runner so
// the JSON written here can be read by that process.
type networkRunnerConfig struct {
	gvtypes.Configuration
	Socket             string `json:"socket"`
	NotificationSocket string `json:"notification_socket,omitempty"`
}

func buildGvisorConfig(ctx context.Context, plan networkResourceModel) (*networkRunnerConfig, diag.Diagnostics) {
	var diags diag.Diagnostics

	const (
		hostLoopback   = "127.0.0.1"
		defaultLinkMTU = 1500
	)

	config := &networkRunnerConfig{
		Socket: plan.SocketPath.ValueString(),
		Configuration: gvtypes.Configuration{
			MTU:               defaultLinkMTU,
			Subnet:            plan.Subnet.ValueString(),
			GatewayIP:         plan.GatewayIP.ValueString(),
			GatewayMacAddress: plan.GatewayMAC.ValueString(),
			DHCPStaticLeases:  map[string]string{},
			DNS:               []gvtypes.Zone{},
			Forwards:          map[string]string{},
			NAT:               map[string]string{hostLoopback: hostLoopback},
			Protocol:          gvtypes.VfkitProtocol,
		},
	}

	dnsByName := map[string]string{}

	for i, lease := range plan.Leases {
		mac := lease.MACAddress.ValueString()
		ip := lease.IPAddress.ValueString()
		name := lease.Hostname.ValueString()
		if mac == "" || ip == "" || name == "" {
			message := fmt.Sprintf("lease[%d]: hostname, ip_address, and mac_address must all be set after planning", i)
			diags.AddError("Invalid lease block", message)
			return nil, diags
		}
		if existing, ok := config.DHCPStaticLeases[ip]; ok && existing != mac {
			message := fmt.Sprintf("lease[%d]: IP %s is already mapped to MAC %s; cannot also map to %s", i, ip, existing, mac)
			diags.AddError("Duplicate DHCP static lease", message)
			return nil, diags
		}
		for existingIP, existingMAC := range config.DHCPStaticLeases {
			if existingMAC == mac {
				message := fmt.Sprintf("lease[%d]: MAC %s is already assigned to IP %s", i, mac, existingIP)
				diags.AddError("Duplicate MAC address in lease", message)
				return nil, diags
			}
		}
		config.DHCPStaticLeases[ip] = mac
		dnsByName[name] = ip
	}

	for name, ip := range dnsByName {
		config.DNS = append(config.DNS, gvtypes.Zone{
			Name: name,
			Records: []gvtypes.Record{
				{IP: net.ParseIP(ip)},
			},
		})
	}

	for _, pf := range plan.PortForwards {
		config.Forwards[pf.Host.ValueString()] = pf.VM.ValueString()
	}

	if !plan.DNSSearchDomains.IsNull() && !plan.DNSSearchDomains.IsUnknown() {
		var domains []string
		diags.Append(plan.DNSSearchDomains.ElementsAs(ctx, &domains, false)...)
		if diags.HasError() {
			return nil, diags
		}
		config.DNSSearchDomains = domains
	}

	return config, diags
}

func networkSocketPath(providerName, networkName string) (string, error) {
	const socketPathFormat = "/tmp/%s-%s-%s.sock"

	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", fmt.Errorf("failed to generate socket path nonce: %w", err)
	}
	randomSuffix := hex.EncodeToString(nonce[:])
	return fmt.Sprintf(socketPathFormat, providerName, networkName, randomSuffix), nil
}

// generateGatewayMAC generates a locally administered unicast MAC address for
// the virtual gateway interface. The first 3 bytes are derived from a SHA-256
// hash of the network name (stable prefix for identification), and the last
// 3 bytes are cryptographically random (collision-safe suffix). The result is
// stable within a single resource lifetime because it is stored in Terraform
// state on Create and never regenerated.
func generateGatewayMAC(networkName string) (string, error) {
	hash := sha256.Sum256([]byte(networkName))
	return generateLocalMAC(hash[:3])
}
