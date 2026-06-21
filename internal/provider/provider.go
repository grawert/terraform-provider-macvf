package provider

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &macvfProvider{}

func New(version, providerName string) func() provider.Provider {
	return func() provider.Provider {
		return &macvfProvider{version: version, providerName: providerName}
	}
}

type macvfProvider struct {
	version      string
	providerName string
}

type macvfProviderModel struct {
	CacheDir  types.String `tfsdk:"cache_dir"`
	VfkitPath types.String `tfsdk:"vfkit_path"`
}

// providerData is passed to each resource via Configure.
type providerData struct {
	ProviderName      string
	VfkitPath         string
	NetworkRunnerPath string
}

func (p *macvfProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "macvf"
	resp.Version = p.version
}

func (p *macvfProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Attributes: map[string]schema.Attribute{
			"cache_dir": schema.StringAttribute{
				Description: fmt.Sprintf(
					"Directory for caching extracted provider binaries. "+
						"Defaults to ~/Library/Caches/%s. Use pathexpand() for paths containing ~.",
					p.providerName,
				),
				Optional: true,
			},
			"vfkit_path": schema.StringAttribute{
				Description: "Path to the vfkit executable. " +
					"When unset, the provider uses the embedded vfkit binary. " +
					"Set this to use a specific vfkit installation instead of the bundled one.",
				Optional: true,
			},
		},
	}
}

func (p *macvfProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config macvfProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var cacheDir string
	if config.CacheDir.IsNull() || config.CacheDir.IsUnknown() || config.CacheDir.ValueString() == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			resp.Diagnostics.AddError("Failed to determine home directory", err.Error())
			return
		}
		cacheDir = filepath.Join(home, "Library", "Caches", p.providerName)
	} else {
		cacheDir = config.CacheDir.ValueString()
	}

	var vfkitPath string
	if !config.VfkitPath.IsNull() && !config.VfkitPath.IsUnknown() && config.VfkitPath.ValueString() != "" {
		vfkitPath = config.VfkitPath.ValueString()
	} else {
		var err error
		vfkitPath, err = extractVfkit(cacheDir)
		if err != nil {
			message := "Failed to extract or locate vfkit. " +
				"macvf_instance resources will fail to create. Error: " + err.Error()
			resp.Diagnostics.AddWarning("vfkit not available", message)
			vfkitPath = ""
		}
	}

	networkRunnerPath, err := extractNetworkRunner(cacheDir)
	if err != nil {
		message := "Failed to extract or locate network-runner. " +
			"macvf_network resources will fail to create. Error: " + err.Error()
		resp.Diagnostics.AddWarning("network-runner not available", message)
		networkRunnerPath = ""
	}

	data := &providerData{
		ProviderName:      p.providerName,
		VfkitPath:         vfkitPath,
		NetworkRunnerPath: networkRunnerPath,
	}
	resp.ResourceData = data
	resp.DataSourceData = data
}

func (p *macvfProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return nil
}

func (p *macvfProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewPoolResource,
		NewDiskResource,
		NewNetworkResource,
		NewInstanceResource,
		NewCloudInitDiskResource,
	}
}
