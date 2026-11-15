package provider

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

func TestNormalizeObject(t *testing.T) {
	attrs := map[string]schema.Attribute{
		"id":       schema.StringAttribute{Computed: true},
		"name":     schema.StringAttribute{Required: true},
		"email":    schema.StringAttribute{Optional: true},
		"tags":     schema.SetAttribute{Optional: true, ElementType: types.StringType},
		"required": schema.ListAttribute{Required: true, ElementType: types.StringType},
		"projects": schema.SetNestedAttribute{Optional: true, NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"key":      schema.StringAttribute{Required: true},
				"branches": schema.SetAttribute{Optional: true, ElementType: types.StringType},
				"note":     schema.StringAttribute{Optional: true},
			},
		}},
		"added": schema.StringAttribute{Optional: true},
	}
	blocks := map[string]schema.Block{
		"setting": schema.ListNestedBlock{NestedObject: schema.NestedBlockObject{
			Attributes: map[string]schema.Attribute{
				"key":   schema.StringAttribute{Required: true},
				"value": schema.StringAttribute{Optional: true},
			},
		}},
		"absent": schema.ListNestedBlock{NestedObject: schema.NestedBlockObject{}},
	}
	in := `{
		"id": "", "name": "", "email": "", "tags": [], "required": [],
		"projects": [{"key": "a", "branches": [], "note": ""}],
		"setting": [{"key": "k", "value": ""}],
		"removed_in_newer_release": "x"
	}`
	want := `{
		"id": null, "name": "", "email": null, "tags": null, "required": [],
		"projects": [{"key": "a", "branches": null, "note": null}],
		"setting": [{"key": "k", "value": null}],
		"added": null, "absent": []
	}`
	var got, exp map[string]any
	if err := json.Unmarshal([]byte(in), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &exp); err != nil {
		t.Fatal(err)
	}
	normalizeObject(got, attrs, blocks)
	if !reflect.DeepEqual(got, exp) {
		g, _ := json.Marshal(got)
		t.Fatalf("normalizeObject:\n got %s\nwant %s", g, want)
	}
}

// Every resource upgrades jdamata state through its own schema, and the
// result decodes into that schema.
func TestUpgradeFromJdamata_everyResource(t *testing.T) {
	ctx := context.Background()
	p := New("test")()
	for _, f := range p.Resources(ctx) {
		r := f()
		var md resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "sonarqube"}, &md)
		t.Run(md.TypeName, func(t *testing.T) {
			var sr resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			if sr.Schema.Version != schemaVersion {
				t.Fatalf("schema version %d, want %d", sr.Schema.Version, schemaVersion)
			}
			u, ok := r.(resource.ResourceWithUpgradeState)
			if !ok {
				t.Fatal("no UpgradeState")
			}
			up, ok := u.UpgradeState(ctx)[0]
			if !ok {
				t.Fatal("no upgrader from version 0")
			}
			// Minimal SDK-style state: every attribute "" or missing.
			raw := map[string]any{"id": "x"}
			for k, a := range sr.Schema.Attributes {
				if _, isString := a.(schema.StringAttribute); isString {
					raw[k] = ""
				}
			}
			js, _ := json.Marshal(raw)
			var resp resource.UpgradeStateResponse
			up.StateUpgrader(ctx, resource.UpgradeStateRequest{RawState: &tfprotov6.RawState{JSON: js}}, &resp)
			if resp.Diagnostics.HasError() {
				t.Fatalf("upgrade: %v", resp.Diagnostics)
			}
			if _, err := resp.DynamicValue.Unmarshal(sr.Schema.Type().TerraformType(ctx)); err != nil {
				t.Fatalf("upgraded state does not decode: %v", err)
			}
		})
	}
}
