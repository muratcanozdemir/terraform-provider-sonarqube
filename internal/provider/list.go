package provider

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/list"
	listschema "github.com/hashicorp/terraform-plugin-framework/list/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// List resources (Terraform 1.14+, `terraform query`) find existing
// SonarQube objects so that they can be imported, e.g. with
// `terraform query -generate-config-out=...`.
//
// Each one only enumerates objects and their identities. With
// include_resource, every object goes through the resource's own
// ImportState and Read, exactly like `terraform import`, so a listed
// resource has the same state an import would give it.

type listItem struct {
	identity []string // values of the resource's identity attributes
	display  string
}

type listSpec struct {
	typeName    string // without the provider prefix, e.g. "_project"
	newResource func() resource.Resource
	description string
	filters     map[string]listschema.Attribute
	enumerate   func(ctx context.Context, c *sonarqube.Client, cfg tfsdk.Config) ([]listItem, diag.Diagnostics)
}

type listResource struct {
	spec listSpec
	p    *providerData
}

var _ list.ListResourceWithConfigure = (*listResource)(nil)

func newList(spec listSpec) func() list.ListResource {
	return func() list.ListResource { return &listResource{spec: spec} }
}

func (l *listResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + l.spec.typeName
}

func (l *listResource) ListResourceConfigSchema(_ context.Context, _ list.ListResourceSchemaRequest, resp *list.ListResourceSchemaResponse) {
	resp.Schema = listschema.Schema{Description: l.spec.description, Attributes: l.spec.filters}
}

func (l *listResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	p, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *providerData, got %T.", req.ProviderData))
		return
	}
	l.p = p
}

func (l *listResource) List(ctx context.Context, req list.ListRequest, stream *list.ListResultsStream) {
	if l.p == nil {
		stream.Results = list.ListResultsStreamDiagnostics(diag.Diagnostics{diag.NewErrorDiagnostic(
			"Provider not configured", "The provider must be configured to list resources.")})
		return
	}
	items, diags := l.spec.enumerate(ctx, l.p.client, req.Config)
	if diags.HasError() {
		stream.Results = list.ListResultsStreamDiagnostics(diags)
		return
	}
	slices.SortFunc(items, func(a, b listItem) int { return cmp.Compare(a.display, b.display) })
	if req.Limit > 0 && int64(len(items)) > req.Limit {
		items = items[:req.Limit]
	}
	stream.Results = func(push func(list.ListResult) bool) {
		for _, item := range items {
			if !push(l.result(ctx, req, item)) {
				return
			}
		}
	}
}

func (l *listResource) result(ctx context.Context, req list.ListRequest, item listItem) list.ListResult {
	res := req.NewListResult(ctx)
	res.DisplayName = item.display
	r, ok := l.spec.newResource().(*identityResource)
	if !ok {
		res.Diagnostics.AddError("Provider error", l.spec.typeName+" has no resource identity.")
		return res
	}
	for i, a := range r.spec.attrs {
		res.Diagnostics.Append(res.Identity.SetAttribute(ctx, path.Root(a.name), item.identity[i])...)
	}
	if !req.IncludeResource || res.Diagnostics.HasError() {
		return res
	}
	var cr resource.ConfigureResponse
	r.Configure(ctx, resource.ConfigureRequest{ProviderData: l.p}, &cr)
	res.Diagnostics.Append(cr.Diagnostics...)

	empty := tfsdk.State{Schema: req.ResourceSchema, Raw: tftypes.NewValue(req.ResourceSchema.Type().TerraformType(ctx), nil)}
	imp := resource.ImportStateResponse{State: empty, Identity: res.Identity}
	r.ImportState(ctx, resource.ImportStateRequest{ID: r.spec.id(item.identity)}, &imp)
	res.Diagnostics.Append(imp.Diagnostics...)
	if res.Diagnostics.HasError() {
		return res
	}
	read := resource.ReadResponse{State: imp.State, Identity: res.Identity}
	r.Read(ctx, resource.ReadRequest{State: imp.State, Identity: res.Identity}, &read)
	res.Diagnostics.Append(read.Diagnostics...)
	if read.State.Raw.IsNull() && !res.Diagnostics.HasError() {
		res.Diagnostics.AddError("Object disappeared", item.display+" was deleted while it was being listed.")
	}
	res.Resource = &tfsdk.Resource{Schema: read.State.Schema, Raw: read.State.Raw}
	return res
}

// ---------------------------------------------------------------------------
// The list resources

func stringFilter(cfg tfsdk.Config, name string, diags *diag.Diagnostics) string {
	var v types.String
	diags.Append(cfg.GetAttribute(context.Background(), path.Root(name), &v)...)
	return v.ValueString()
}

func boolFilter(cfg tfsdk.Config, name string, diags *diag.Diagnostics) bool {
	var v types.Bool
	diags.Append(cfg.GetAttribute(context.Background(), path.Root(name), &v)...)
	return v.ValueBool()
}

var searchFilter = map[string]listschema.Attribute{
	"search": listschema.StringAttribute{Optional: true, Description: "Only objects whose key, login or name contains this text."},
}

var builtInFilter = listschema.BoolAttribute{Optional: true, Description: "Include built-in objects, which cannot be managed. Defaults to false."}

func apiDiags(summary string, err error) diag.Diagnostics {
	var d diag.Diagnostics
	apiError(&d, summary, err)
	return d
}

func (p *sonarqubeProvider) ListResources(context.Context) []func() list.ListResource {
	return []func() list.ListResource{
		newList(listSpec{
			typeName: "_project", newResource: withIdentity(newProjectResource, projectIdentity),
			description: "Lists projects.", filters: searchFilter,
			enumerate: func(ctx context.Context, c *sonarqube.Client, cfg tfsdk.Config) ([]listItem, diag.Diagnostics) {
				var diags diag.Diagnostics
				q := stringFilter(cfg, "search", &diags)
				projects, err := collect(ctx, 500, func(ctx context.Context, page, size int) ([]*sonarqube.ProjectsSearchResponseComponent, int64, error) {
					res, _, err := c.Projects.Search(ctx, &sonarqube.ProjectsSearchOptions{Q: q, Qualifiers: []string{"TRK"}, P: &page, Ps: &size})
					if err != nil {
						return nil, 0, err
					}
					return res.Components, total(res.Paging), nil
				})
				if err != nil {
					return nil, apiDiags("Unable to list projects", err)
				}
				var items []listItem
				for _, p := range projects {
					items = append(items, listItem{identity: []string{p.Key}, display: p.Key})
				}
				return items, diags
			},
		}),
		newList(listSpec{
			typeName: "_qualityprofile", newResource: withIdentity(newQualityProfileResource, profileIdentity),
			description: "Lists quality profiles.",
			filters: map[string]listschema.Attribute{
				"language":         listschema.StringAttribute{Optional: true, Description: "Only profiles of this language."},
				"include_built_in": builtInFilter,
			},
			enumerate: func(ctx context.Context, c *sonarqube.Client, cfg tfsdk.Config) ([]listItem, diag.Diagnostics) {
				var diags diag.Diagnostics
				lang, builtIn := stringFilter(cfg, "language", &diags), boolFilter(cfg, "include_built_in", &diags)
				res, _, err := c.Qualityprofiles.Search(ctx, &sonarqube.QualityprofilesSearchOptions{Language: lang})
				if err != nil {
					return nil, apiDiags("Unable to list quality profiles", err)
				}
				var items []listItem
				for _, p := range res.Profiles {
					if p.IsBuiltIn && !builtIn {
						continue
					}
					items = append(items, listItem{identity: []string{p.Language, p.Name}, display: p.Language + "/" + p.Name})
				}
				return items, diags
			},
		}),
		newList(listSpec{
			typeName: "_qualitygate", newResource: withIdentity(newQualityGateResource, nameIdentity("Quality gate")),
			description: "Lists quality gates.",
			filters:     map[string]listschema.Attribute{"include_built_in": builtInFilter},
			enumerate: func(ctx context.Context, c *sonarqube.Client, cfg tfsdk.Config) ([]listItem, diag.Diagnostics) {
				var diags diag.Diagnostics
				builtIn := boolFilter(cfg, "include_built_in", &diags)
				res, _, err := c.Qualitygates.List(ctx)
				if err != nil {
					return nil, apiDiags("Unable to list quality gates", err)
				}
				var items []listItem
				for _, g := range res.Qualitygates {
					if g.IsBuiltIn && !builtIn {
						continue
					}
					items = append(items, listItem{identity: []string{g.Name}, display: g.Name})
				}
				return items, diags
			},
		}),
		newList(listSpec{
			typeName: "_group", newResource: withIdentity(newGroupResource, nameIdentity("Group")),
			description: "Lists groups.", filters: searchFilter,
			enumerate: func(ctx context.Context, c *sonarqube.Client, cfg tfsdk.Config) ([]listItem, diag.Diagnostics) {
				var diags diag.Diagnostics
				groups, err := searchGroups(ctx, c, stringFilter(cfg, "search", &diags))
				if err != nil {
					return nil, apiDiags("Unable to list groups", err)
				}
				var items []listItem
				for _, g := range groups {
					items = append(items, listItem{identity: []string{g.Name}, display: g.Name})
				}
				return items, diags
			},
		}),
		newList(listSpec{
			typeName: "_user", newResource: withIdentity(newUserResource, userIdentity),
			description: "Lists active users.", filters: searchFilter,
			enumerate: func(ctx context.Context, c *sonarqube.Client, cfg tfsdk.Config) ([]listItem, diag.Diagnostics) {
				var diags diag.Diagnostics
				users, err := searchUsers(ctx, c, sonarqube.UsersManagementSearchOptions{Q: stringFilter(cfg, "search", &diags)})
				if err != nil {
					return nil, apiDiags("Unable to list users", err)
				}
				var items []listItem
				for _, u := range users {
					items = append(items, listItem{identity: []string{u.Login}, display: u.Login})
				}
				return items, diags
			},
		}),
	}
}
