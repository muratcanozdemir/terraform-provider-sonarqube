package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type serverDataSource struct{ dataSourceBase }

func newServerDataSource() datasource.DataSource { return &serverDataSource{} }

type serverModel struct {
	ID      types.String `tfsdk:"id"`
	Version types.String `tfsdk:"version"`
	Edition types.String `tfsdk:"edition"`
	Status  types.String `tfsdk:"status"`
}

func (d *serverDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_server"
}

func (d *serverDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Version, edition and status of the server, e.g. to gate configuration on edition.",
		Attributes: map[string]schema.Attribute{
			"id":      schema.StringAttribute{Computed: true, Description: "Server ID."},
			"version": schema.StringAttribute{Computed: true, Description: "Server version, e.g. `26.11.0.134175`."},
			"edition": schema.StringAttribute{Computed: true, Description: "`community`, `developer`, `enterprise` or `datacenter`."},
			"status":  schema.StringAttribute{Computed: true, Description: "Server status, e.g. `UP`."},
		},
	}
}

func (d *serverDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	info, err := d.p.info.get(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Unable to read server information", err.Error())
		return
	}
	st, _, err := d.c().System.Status(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "Unable to read server status", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &serverModel{
		ID:      types.StringValue(string(st.ID)),
		Version: types.StringValue(info.Version),
		Edition: types.StringValue(info.Edition),
		Status:  types.StringValue(st.Status),
	})...)
}
