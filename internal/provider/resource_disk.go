package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"syscall"
	"unsafe"

	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource = &diskResource{}
)

func NewDiskResource() resource.Resource {
	return &diskResource{}
}

type diskResource struct{}

type diskResourceModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	PoolID     types.String `tfsdk:"pool_id"`
	Size       types.String `tfsdk:"size"`
	Source     types.String `tfsdk:"source"`
	BaseDiskID types.String `tfsdk:"base_disk_id"`
}

func (r *diskResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_disk"
}

func (r *diskResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A virtual disk for a MacVF instance.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Full path to the disk file.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the disk.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"pool_id": schema.StringAttribute{
				Description: "The ID of the storage pool where the disk will be created.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"size": schema.StringAttribute{
				Description: "Size of the disk (e.g. \"20GiB\", \"10GB\"). Required when creating a blank disk. " +
					"When used with source or base_disk_id, grows the disk to this size after copying; " +
					"must be >= the source size.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source": schema.StringAttribute{
				Description: "Path to an existing disk image to copy into the pool on create. " +
					"Mutually exclusive with base_disk_id. Use pathexpand() for paths containing ~.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"base_disk_id": schema.StringAttribute{
				Description: "ID of an existing macvf_disk to clone as the starting point for this disk. " +
					"Mutually exclusive with source.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *diskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan diskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	hasSource := !plan.Source.IsNull() && !plan.Source.IsUnknown()
	hasBase := !plan.BaseDiskID.IsNull() && !plan.BaseDiskID.IsUnknown()
	hasSize := !plan.Size.IsNull() && !plan.Size.IsUnknown()

	if hasSource && hasBase {
		resp.Diagnostics.AddError("Invalid configuration", "source and base_disk_id are mutually exclusive.")
		return
	}
	if !hasSource && !hasBase && !hasSize {
		resp.Diagnostics.AddAttributeError(path.Root("size"), "Missing required attribute",
			"size is required when neither source nor base_disk_id is set.")
		return
	}

	var targetBytes int64
	if hasSize {
		parsed, err := parseBytesize(plan.Size.ValueString())
		if err != nil {
			resp.Diagnostics.AddAttributeError(path.Root("size"), "Invalid size", err.Error())
			return
		}
		if parsed <= 0 {
			resp.Diagnostics.AddAttributeError(path.Root("size"), "Invalid size", "size must be greater than zero.")
			return
		}
		targetBytes = int64(parsed)
	}

	diskPath := filepath.Join(plan.PoolID.ValueString(), plan.Name.ValueString()+".raw")
	tflog.Info(ctx, "creating disk", map[string]any{
		"name":    plan.Name.ValueString(),
		"pool_id": plan.PoolID.ValueString(),
	})

	switch {
	case hasSource:
		tflog.Info(ctx, "copying source image", map[string]any{
			"source": plan.Source.ValueString(),
			"dest":   diskPath,
		})
		if err := cloneDisk(plan.Source.ValueString(), diskPath); err != nil {
			resp.Diagnostics.AddError("failed to copy source image", err.Error())
			return
		}
	case hasBase:
		tflog.Info(ctx, "cloning base disk", map[string]any{
			"base": plan.BaseDiskID.ValueString(),
			"dest": diskPath,
		})
		if err := cloneDisk(plan.BaseDiskID.ValueString(), diskPath); err != nil {
			resp.Diagnostics.AddError("failed to clone base disk", err.Error())
			return
		}
	default:
		file, err := os.Create(diskPath)
		if err != nil {
			resp.Diagnostics.AddError("failed to create disk file", err.Error())
			return
		}
		defer file.Close()
		if err := file.Truncate(targetBytes); err != nil {
			resp.Diagnostics.AddError("failed to truncate disk file", err.Error())
			return
		}
	}

	if (hasSource || hasBase) && hasSize {
		info, err := os.Stat(diskPath)
		if err != nil {
			resp.Diagnostics.AddError("failed to stat disk after copy", err.Error())
			return
		}
		if targetBytes < info.Size() {
			_ = os.Remove(diskPath)
			message := fmt.Sprintf(
				"%q (%d bytes) is less than the source size %d bytes; refusing to shrink.",
				plan.Size.ValueString(), targetBytes, info.Size(),
			)
			resp.Diagnostics.AddAttributeError(path.Root("size"), "size too small", message)
			return
		}
		if targetBytes > info.Size() {
			if err := os.Truncate(diskPath, targetBytes); err != nil {
				resp.Diagnostics.AddError("failed to grow disk", err.Error())
				return
			}
		}
	}

	plan.ID = types.StringValue(diskPath)
	tflog.Info(ctx, "disk created", map[string]any{"id": diskPath})
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

func (r *diskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state diskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "reading disk", map[string]any{"id": state.ID.ValueString()})

	if _, err := os.Stat(state.ID.ValueString()); os.IsNotExist(err) {
		tflog.Warn(ctx, "disk file not found, removing from state", map[string]any{"id": state.ID.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
}

func (r *diskResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	message := "Disk resources cannot be updated. " +
		"Please destroy and recreate the resource with the new configuration."
	resp.Diagnostics.AddError("Updates not supported", message)
}

func (r *diskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state diskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "deleting disk", map[string]any{
		"id":   state.ID.ValueString(),
		"name": state.Name.ValueString(),
	})

	if err := os.Remove(state.ID.ValueString()); err != nil && !os.IsNotExist(err) {
		resp.Diagnostics.AddError("failed to delete disk file", err.Error())
	}
}

// cloneDisk copies src to dst. It first attempts an APFS copy-on-write clone
// via clonefileat(2) (Darwin syscall 501), which is instant and consumes no
// additional disk space until the VM writes to the cloned sectors. On
// non-APFS volumes the syscall returns ENOTSUP and the function falls back to
// a regular byte copy.
func cloneDisk(src, dst string) error {
	if err := clonefileat(src, dst); err == nil {
		return nil
	}
	return copyFile(src, dst)
}

// clonefileat calls the Darwin clonefileat(2) syscall with AT_FDCWD for both
// directory descriptors, equivalent to the libc clonefile(src, dst, 0) call.
func clonefileat(src, dst string) error {
	srcPtr, err := syscall.BytePtrFromString(src)
	if err != nil {
		return err
	}
	dstPtr, err := syscall.BytePtrFromString(dst)
	if err != nil {
		return err
	}
	const sysClonefileat = 501
	atFDCWD := -2 // AT_FDCWD; must be a variable so uintptr() conversion happens at runtime
	_, _, errno := syscall.Syscall6(
		sysClonefileat,
		uintptr(atFDCWD),
		uintptr(unsafe.Pointer(srcPtr)),
		uintptr(atFDCWD),
		uintptr(unsafe.Pointer(dstPtr)),
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}
	return nil
}
