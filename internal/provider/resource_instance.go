package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource              = &instanceResource{}
	_ resource.ResourceWithConfigure = &instanceResource{}
)

func NewInstanceResource() resource.Resource {
	return &instanceResource{}
}

type instanceResource struct {
	vfkitPath string
}

func (r *instanceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		message := fmt.Sprintf("Expected *providerData, got %T", req.ProviderData)
		resp.Diagnostics.AddError("Unexpected provider data type", message)
		return
	}
	r.vfkitPath = data.VfkitPath
}

type instanceResourceModel struct {
	ID                types.String                    `tfsdk:"id"`
	Name              types.String                    `tfsdk:"name"`
	VCPUs             types.Int64                     `tfsdk:"vcpus"`
	Memory            types.String                    `tfsdk:"memory"`
	PID               types.Int64                     `tfsdk:"pid"`
	ConsoleLogPath    types.String                    `tfsdk:"console_log_path"`
	VfkitLogPath      types.String                    `tfsdk:"vfkit_log_path"`
	CloudInitDiskID   types.String                    `tfsdk:"cloud_init_disk_id"`
	NetworkInterfaces []instanceNetworkInterfaceModel `tfsdk:"network_interface"`
	DiskAttachments   []instanceDiskAttachmentModel   `tfsdk:"disk_attachment"`
	LinuxBoot         *instanceLinuxBootModel         `tfsdk:"linux_boot"`
}

type instanceLinuxBootModel struct {
	KernelPath types.String `tfsdk:"kernel_path"`
	InitrdPath types.String `tfsdk:"initrd_path"`
	Cmdline    types.String `tfsdk:"cmdline"`
}

type instanceNetworkInterfaceModel struct {
	Type       types.String `tfsdk:"type"`
	NetworkID  types.String `tfsdk:"network_id"`
	MACAddress types.String `tfsdk:"mac_address"`
}

type instanceDiskAttachmentModel struct {
	DiskID types.String `tfsdk:"disk_id"`
	IsBoot types.Bool   `tfsdk:"is_boot"`
}

func (r *instanceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_instance"
}

func (r *instanceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A virtual machine instance.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Unique identifier for this resource.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the instance.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vcpus": schema.Int64Attribute{
				Description: "The number of vCPUs for the instance.",
				Required:    true,
				Validators:  []validator.Int64{positiveInt64Validator{}},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.RequiresReplace(),
				},
			},
			"memory": schema.StringAttribute{
				Description: "The amount of memory for the instance. Accepts the same suffixes as disk size: " +
					"B, KB, MB, GB, TB, PB, EB (e.g. \"2GB\", \"512MB\"). All units are binary (1 GB = 1024³ bytes). " +
					"Values are rounded down to the nearest MiB by vfkit.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"pid": schema.Int64Attribute{
				Description: "PID of the running vfkit process.",
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"console_log_path": schema.StringAttribute{
				Description: "Path to a file where the VM's serial console output is written " +
					"(e.g. \"${path.module}/my-vm-console.log\"). When set, a virtio-serial device is added " +
					"and the guest writes to it via /dev/hvc0. " +
					"Useful for capturing boot messages and cloud-init output.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"vfkit_log_path": schema.StringAttribute{
				Description: "Path to a file where vfkit's own log output (stderr) is written " +
					"(e.g. \"${path.module}/vm01-vfkit.log\"). When omitted the log is placed next to " +
					"console_log_path if set, otherwise in the system temp directory.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"cloud_init_disk_id": schema.StringAttribute{
				Description: "The id of a macvf_cloudinit_disk resource. When set, vfkit's --cloud-init flag " +
					"is used to supply user-data, meta-data, and (if present) network-config to the instance.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
		Blocks: map[string]schema.Block{
			"linux_boot": schema.SingleNestedBlock{
				Description: "Boot via direct kernel boot. When omitted, EFI boot is used.",
				Attributes: map[string]schema.Attribute{
					"kernel_path": schema.StringAttribute{
						Description: "Path to the Linux kernel image. Use pathexpand() for paths containing ~.",
						Optional:    true,
					},
					"initrd_path": schema.StringAttribute{
						Description: "Path to the initial ramdisk. Use pathexpand() for paths containing ~.",
						Optional:    true,
					},
					"cmdline": schema.StringAttribute{
						Description: "Kernel command line arguments.",
						Optional:    true,
					},
				},
			},
			"network_interface": schema.ListNestedBlock{
				Description: "Network interface to attach to the instance.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"type": schema.StringAttribute{
							Description: "Attachment type: \"gvisor\" (default) connects via a macvf_network Unix socket " +
								"using gvisor-tap-vsock; \"nat\" uses Apple's built-in NAT " +
								"(outbound internet only, VM is not reachable from the host).",
							Optional:    true,
							Computed:    true,
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.RequiresReplace(),
								stringplanmodifier.UseStateForUnknown(),
							},
						},
						"network_id": schema.StringAttribute{
							Description: "The ID of the macvf_network to attach. " +
								"Required when type is \"gvisor\", ignored when type is \"nat\".",
							Optional:    true,
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.RequiresReplace(),
							},
						},
						"mac_address": schema.StringAttribute{
							Description: "MAC address of this network interface. Auto-generated if not set; " +
								"stored in state so it remains stable across plans. " +
								"Set explicitly to match a dhcp_static_leases or dns_hosts entry on macvf_network " +
								"for a stable IP and DNS hostname.",
							Optional:    true,
							Computed:    true,
							PlanModifiers: []planmodifier.String{
								stringplanmodifier.RequiresReplace(),
								stringplanmodifier.UseStateForUnknown(),
							},
						},
					},
				},
			},
			"disk_attachment": schema.ListNestedBlock{
				Description: "Disk to attach to the instance.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"disk_id": schema.StringAttribute{
							Description: "The ID of the disk to attach.",
							Required:    true,
						},
						"is_boot": schema.BoolAttribute{
							Description: "Whether this is the boot disk.",
							Required:    true,
						},
					},
				},
			},
		},
	}
}

func (r *instanceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan instanceResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.LinuxBoot != nil {
		linuxBootPath := path.Root("linux_boot")
		if plan.LinuxBoot.KernelPath.IsNull() || plan.LinuxBoot.KernelPath.ValueString() == "" {
			resp.Diagnostics.AddAttributeError(
				linuxBootPath.AtName("kernel_path"),
				"Missing required attribute",
				"kernel_path is required when linux_boot is set.",
			)
		}
		if plan.LinuxBoot.InitrdPath.IsNull() || plan.LinuxBoot.InitrdPath.ValueString() == "" {
			resp.Diagnostics.AddAttributeError(
				linuxBootPath.AtName("initrd_path"),
				"Missing required attribute",
				"initrd_path is required when linux_boot is set.",
			)
		}
		if plan.LinuxBoot.Cmdline.IsNull() || plan.LinuxBoot.Cmdline.ValueString() == "" {
			resp.Diagnostics.AddAttributeError(
				linuxBootPath.AtName("cmdline"),
				"Missing required attribute",
				"cmdline is required when linux_boot is set.",
			)
		}
		if resp.Diagnostics.HasError() {
			return
		}
	}

	memBytes, err := parseBytesize(plan.Memory.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Invalid memory value", err.Error())
		return
	}
	memMiB := int64(memBytes) / (1024 * 1024)

	tflog.Info(ctx, "creating instance", map[string]any{
		"name":   plan.Name.ValueString(),
		"vcpus":  plan.VCPUs.ValueInt64(),
		"memory": plan.Memory.ValueString(),
	})

	if r.vfkitPath == "" {
		message := "vfkit executable not found. " +
			"This provider requires vfkit to be installed or embedded."
		resp.Diagnostics.AddError("vfkit not found", message)
		return
	}

	var bootloaderCmd string
	if plan.LinuxBoot != nil {
		bootloaderCmd = fmt.Sprintf("linux,kernel=%s,initrd=%s,cmdline=%s",
			plan.LinuxBoot.KernelPath.ValueString(),
			plan.LinuxBoot.InitrdPath.ValueString(),
			plan.LinuxBoot.Cmdline.ValueString(),
		)
	} else {
		efiPath, err := efiVarsPath(plan.Name.ValueString(), plan.DiskAttachments)
		if err != nil {
			resp.Diagnostics.AddError("Invalid EFI boot configuration", err.Error())
			return
		}
		bootloaderCmd = fmt.Sprintf("efi,variable-store=%s,create", efiPath)
	}

	args := []string{
		"--cpus", strconv.FormatInt(plan.VCPUs.ValueInt64(), 10),
		"--memory", strconv.FormatInt(memMiB, 10),
		"--bootloader", bootloaderCmd,
	}

	for i := range plan.NetworkInterfaces {
		nic := &plan.NetworkInterfaces[i]

		if nic.Type.IsNull() || nic.Type.IsUnknown() || nic.Type.ValueString() == "" {
			nic.Type = types.StringValue("gvisor")
		}
		nicType := nic.Type.ValueString()

		if nicType == "gvisor" && (nic.NetworkID.IsNull() || nic.NetworkID.IsUnknown() || nic.NetworkID.ValueString() == "") {
			resp.Diagnostics.AddError("Missing network_id", "network_interface.network_id is required when type is \"gvisor\".")
			return
		}

		if nic.MACAddress.IsNull() || nic.MACAddress.IsUnknown() {
			mac, err := generateLocalMAC(nil)
			if err != nil {
				resp.Diagnostics.AddError("Failed to generate MAC address for network interface", err.Error())
				return
			}
			nic.MACAddress = types.StringValue(mac)
		}

		switch nicType {
		case "nat":
			device := fmt.Sprintf("virtio-net,nat,mac=%s", nic.MACAddress.ValueString())
			args = append(args, "--device", device)
		default:
			device := fmt.Sprintf(
				"virtio-net,unixSocketPath=%s,mac=%s",
				nic.NetworkID.ValueString(), nic.MACAddress.ValueString(),
			)
			args = append(args, "--device", device)
		}
	}

	for _, disk := range plan.DiskAttachments {
		args = append(args, "--device", fmt.Sprintf("virtio-blk,path=%s", disk.DiskID.ValueString()))
	}

	if !plan.CloudInitDiskID.IsNull() && !plan.CloudInitDiskID.IsUnknown() {
		dir := plan.CloudInitDiskID.ValueString()
		cloudInitArg := filepath.Join(dir, "user-data") + "," + filepath.Join(dir, "meta-data")
		if _, err := os.Stat(filepath.Join(dir, "network-config")); err == nil {
			cloudInitArg += "," + filepath.Join(dir, "network-config")
		}
		args = append(args, "--cloud-init", cloudInitArg)
	}

	hasLog := !plan.ConsoleLogPath.IsNull() && !plan.ConsoleLogPath.IsUnknown()
	if hasLog {
		args = append(args, "--device", fmt.Sprintf("virtio-serial,logFilePath=%s", plan.ConsoleLogPath.ValueString()))
	}

	// Always write vfkit stderr to a sidecar log so post-crash errors are
	// inspectable even after the process is detached.
	var stderrLogPath string
	if !plan.VfkitLogPath.IsNull() && !plan.VfkitLogPath.IsUnknown() {
		stderrLogPath = plan.VfkitLogPath.ValueString()
	} else {
		stderrLogName := plan.Name.ValueString() + "-vfkit.log"
		var stderrLogDir string
		if hasLog {
			stderrLogDir = filepath.Dir(plan.ConsoleLogPath.ValueString())
		} else {
			stderrLogDir = os.TempDir()
		}
		stderrLogPath = filepath.Join(stderrLogDir, stderrLogName)
	}
	os.Remove(stderrLogPath)
	cmdLine := fmt.Sprintf("command: %s %s\n\n", r.vfkitPath, strings.Join(args, " "))
	_ = os.WriteFile(stderrLogPath, []byte(cmdLine), 0600)
	stderrFile, fileErr := os.OpenFile(stderrLogPath, os.O_WRONLY|os.O_APPEND, 0600)

	var stderrBuf strings.Builder
	cmd := exec.Command(r.vfkitPath, args...)
	if fileErr == nil {
		cmd.Stderr = io.MultiWriter(&stderrBuf, stderrFile)
	} else {
		cmd.Stderr = &stderrBuf
	}
	if err := cmd.Start(); err != nil {
		if stderrFile != nil {
			stderrFile.Close()
		}
		resp.Diagnostics.AddError("Failed to start vfkit process", err.Error())
		return
	}

	pid := cmd.Process.Pid

	// Wait briefly: if vfkit exits within the window it crashed on bad args.
	// cmd.Wait() reaps the child so the exit status is reliable; kill -0 is
	// not because the PID lingers as a zombie until the parent reaps it.
	// 3 s gives Apple Virtualization.framework time to initialize before
	// vfkit validates device arguments — framework init can take > 500 ms.
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case waitErr := <-exited:
		// cmd.Wait() has returned: all stderr output is now in stderrBuf.
		if stderrFile != nil {
			stderrFile.Close()
		}
		stderr := strings.TrimSpace(stderrBuf.String())
		msg := fmt.Sprintf("vfkit exited immediately after launch (stderr log: %s)", stderrLogPath)
		if waitErr != nil {
			msg += " (" + waitErr.Error() + ")"
		}
		if stderr != "" {
			msg += ": " + stderr
		}
		resp.Diagnostics.AddError("vfkit process died on startup", msg)
		return
	case <-time.After(3 * time.Second):
		if stderrFile != nil {
			stderrFile.Close()
		}
		// Still running — detach and let it live independently.
		if err := cmd.Process.Release(); err != nil {
			resp.Diagnostics.AddError("Failed to detach vfkit process", err.Error())
			return
		}
		tflog.Info(ctx, "vfkit stderr log", map[string]any{"path": stderrLogPath})
	}

	plan.ID = types.StringValue(plan.Name.ValueString())
	plan.PID = types.Int64Value(int64(pid))
	tflog.Info(ctx, "instance started", map[string]any{
		"id":  plan.ID.ValueString(),
		"pid": plan.PID.ValueInt64(),
	})

	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *instanceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state instanceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "reading instance", map[string]any{
		"id":  state.ID.ValueString(),
		"pid": state.PID.ValueInt64(),
	})

	if !isProcessAlive(int(state.PID.ValueInt64())) {
		tflog.Warn(ctx, "instance process not found, removing from state", map[string]any{
			"id":  state.ID.ValueString(),
			"pid": state.PID.ValueInt64(),
		})
		message := fmt.Sprintf("The vfkit process with PID %d is gone.", state.PID.ValueInt64())
		resp.Diagnostics.AddWarning("VM process not found", message)
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *instanceResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	message := "Instance resources cannot be updated. " +
		"Please destroy and recreate the resource."
	resp.Diagnostics.AddError("Updates not supported", message)
}

func (r *instanceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state instanceResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "deleting instance", map[string]any{
		"id":  state.ID.ValueString(),
		"pid": state.PID.ValueInt64(),
	})

	if err := terminateProcess(int(state.PID.ValueInt64()), 10*time.Second); err != nil {
		resp.Diagnostics.AddWarning("Failed to stop vfkit process",
			fmt.Sprintf("Could not send SIGTERM to PID %d: %s", state.PID.ValueInt64(), err))
	}

	if state.LinuxBoot == nil {
		if efiPath, err := efiVarsPath(state.Name.ValueString(), state.DiskAttachments); err == nil {
			if err := os.Remove(efiPath); err != nil && !os.IsNotExist(err) {
				resp.Diagnostics.AddWarning("Failed to remove EFI variable store", err.Error())
			}
		}
	}

}

// efiVarsPath derives the NVRAM variable store path from the boot disk location,
// placing it alongside the boot disk in its pool directory.
func efiVarsPath(instanceName string, disks []instanceDiskAttachmentModel) (string, error) {
	for _, disk := range disks {
		if disk.IsBoot.ValueBool() {
			return filepath.Join(filepath.Dir(disk.DiskID.ValueString()), instanceName+".efivars.fd"), nil
		}
	}
	return "", fmt.Errorf("EFI boot requires a disk_attachment with is_boot = true")
}
