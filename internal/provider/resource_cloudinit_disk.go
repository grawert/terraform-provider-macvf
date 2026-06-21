package provider

import (
	"context"
	"os"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.Resource = &cloudInitDiskResource{}

func NewCloudInitDiskResource() resource.Resource {
	return &cloudInitDiskResource{}
}

type cloudInitDiskResource struct{}

type cloudInitDiskResourceModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	PoolID            types.String `tfsdk:"pool_id"`
	UserData          types.String `tfsdk:"user_data"`
	MetaData          types.String `tfsdk:"meta_data"`
	NetworkConfig     types.String `tfsdk:"network_config"`
	UserDataPath      types.String `tfsdk:"user_data_path"`
	MetaDataPath      types.String `tfsdk:"meta_data_path"`
	NetworkConfigPath types.String `tfsdk:"network_config_path"`
}

func (r *cloudInitDiskResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_cloudinit_disk"
}

func (r *cloudInitDiskResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Writes cloud-init configuration files (user-data, meta-data, network-config) to a directory. " +
			"Use the computed *_path attributes in the macvf_instance cloud_init block.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Path to the directory containing the cloud-init files.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "Name for this cloud-init resource; used as the directory name inside the pool.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"pool_id": schema.StringAttribute{
				Description: "The ID of the storage pool where the cloud-init directory will be created.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_data": schema.StringAttribute{
				Description: "Cloud-init user-data content (YAML starting with #cloud-config).",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"meta_data": schema.StringAttribute{
				Description: "Cloud-init meta-data content (YAML). When omitted an empty file is written, " +
					"which is still required by the NoCloud datasource.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"network_config": schema.StringAttribute{
				Description: "Cloud-init network configuration (YAML). " +
					"When omitted cloud-init falls back to its built-in DHCP detection.",
				Optional:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_data_path": schema.StringAttribute{
				Description: "Absolute path to the written user-data file. Pass to macvf_instance cloud_init.user_data_path.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"meta_data_path": schema.StringAttribute{
				Description: "Absolute path to the written meta-data file. Pass to macvf_instance cloud_init.meta_data_path.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"network_config_path": schema.StringAttribute{
				Description: "Absolute path to the written network-config file, or empty when network_config was not set.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *cloudInitDiskResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var model cloudInitDiskResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	dir := filepath.Join(model.PoolID.ValueString(), model.Name.ValueString()+".cloud-init")
	tflog.Info(ctx, "creating cloudinit files", map[string]any{"dir": dir})

	if err := os.MkdirAll(dir, 0755); err != nil {
		resp.Diagnostics.AddError("Failed to create cloud-init directory", err.Error())
		return
	}

	if err := os.WriteFile(filepath.Join(dir, "user-data"), []byte(model.UserData.ValueString()), 0644); err != nil {
		resp.Diagnostics.AddError("Failed to write user-data", err.Error())
		return
	}

	// NoCloud datasource requires meta-data to exist alongside user-data even if empty.
	metaDataContent := ""
	if !model.MetaData.IsNull() && !model.MetaData.IsUnknown() {
		metaDataContent = model.MetaData.ValueString()
	}
	if err := os.WriteFile(filepath.Join(dir, "meta-data"), []byte(metaDataContent), 0644); err != nil {
		resp.Diagnostics.AddError("Failed to write meta-data", err.Error())
		return
	}

	networkConfigPath := ""
	if !model.NetworkConfig.IsNull() && !model.NetworkConfig.IsUnknown() {
		p := filepath.Join(dir, "network-config")
		if err := os.WriteFile(p, []byte(model.NetworkConfig.ValueString()), 0644); err != nil {
			resp.Diagnostics.AddError("Failed to write network-config", err.Error())
			return
		}
		networkConfigPath = p
	}

	model.ID = types.StringValue(dir)
	model.UserDataPath = types.StringValue(filepath.Join(dir, "user-data"))
	model.MetaDataPath = types.StringValue(filepath.Join(dir, "meta-data"))
	model.NetworkConfigPath = types.StringValue(networkConfigPath)

	tflog.Info(ctx, "cloudinit files written", map[string]any{"dir": dir})
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *cloudInitDiskResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var model cloudInitDiskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := os.Stat(model.UserDataPath.ValueString()); err != nil {
		if os.IsNotExist(err) {
			tflog.Warn(ctx, "cloud-init user-data not found, removing from state", map[string]any{
				"path": model.UserDataPath.ValueString(),
			})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to stat user-data", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *cloudInitDiskResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Updates not supported",
		"Cloud-init disk resources cannot be updated. Please destroy and recreate the resource.")
}

func (r *cloudInitDiskResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var model cloudInitDiskResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "deleting cloudinit directory", map[string]any{"dir": model.ID.ValueString()})

	if err := os.RemoveAll(model.ID.ValueString()); err != nil && !os.IsNotExist(err) {
		resp.Diagnostics.AddError("Failed to delete cloud-init directory", err.Error())
	}
}
