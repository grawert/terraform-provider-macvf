package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource = &poolResource{}
)

func NewPoolResource() resource.Resource {
	return &poolResource{}
}

type poolResource struct{}

type poolResourceModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
	Path types.String `tfsdk:"path"`
}

func (r *poolResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_pool"
}

func (r *poolResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A storage pool for MacVF resources.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Unique identifier for this resource.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the storage pool.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"path": schema.StringAttribute{
				Description: "The path to the storage pool directory. Use pathexpand() for paths containing ~.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *poolResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan poolResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "creating pool", map[string]any{
		"name": plan.Name.ValueString(),
		"path": plan.Path.ValueString(),
	})

	path := plan.Path.ValueString()
	if err := os.MkdirAll(path, 0755); err != nil {
		resp.Diagnostics.AddError("failed to create pool directory", err.Error())
		return
	}

	plan.ID = types.StringValue(path)
	tflog.Info(ctx, "pool created", map[string]any{"id": plan.ID.ValueString()})

	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
}

func (r *poolResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state poolResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "reading pool", map[string]any{"id": state.ID.ValueString()})

	if _, err := os.Stat(state.ID.ValueString()); os.IsNotExist(err) {
		tflog.Warn(ctx, "pool directory not found, removing from state", map[string]any{"id": state.ID.ValueString()})
		resp.State.RemoveResource(ctx)
		return
	}

	diags = resp.State.Set(ctx, state)
	resp.Diagnostics.Append(diags...)
}

func (r *poolResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	message := "Pool resources cannot be updated. " +
		"Please destroy and recreate the resource with the new configuration."
	resp.Diagnostics.AddError("Updates not supported", message)
}

func (r *poolResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state poolResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Info(ctx, "deleting pool", map[string]any{
		"id":   state.ID.ValueString(),
		"name": state.Name.ValueString(),
	})

	path := state.Path.ValueString()
	if err := os.RemoveAll(path); err != nil {
		resp.Diagnostics.AddError("failed to delete pool directory", err.Error())
		return
	}
}
