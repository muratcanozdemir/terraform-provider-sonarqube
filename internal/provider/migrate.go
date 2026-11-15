package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
)

// schemaVersion is the version of every resource schema.
//
// Version 0 is state written by jdamata/terraform-provider-sonarqube, which
// uses the same resource type names, so a configuration migrates with
// `terraform state replace-provider`. That provider is built on the Terraform
// Plugin SDK v2, which stores unset optional strings as "" where this
// provider stores null. Read keys some decisions on whether a value is set
// (a setting is single-valued when `value` is set), so the difference would
// otherwise cause spurious changes, and in places real ones, on the first
// apply after migrating.
const schemaVersion = 1

// upgradeFromJdamata returns the upgrader from version 0 for r. It rewrites
// the raw state against r's current schema: "" becomes null for optional
// strings, empty lists, sets and maps become null for optional collections
// (blocks stay as they are, being never null), attributes the schema does not have are dropped (older releases of
// that provider had a few more), and attributes it lacks are added as null.
func upgradeFromJdamata(r resource.Resource) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: func(ctx context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
			if req.RawState == nil || len(req.RawState.JSON) == 0 {
				resp.Diagnostics.AddError("Unable to upgrade state", "The prior state is not in JSON form.")
				return
			}
			var sr resource.SchemaResponse
			r.Schema(ctx, resource.SchemaRequest{}, &sr)
			var obj map[string]any
			if err := json.Unmarshal(req.RawState.JSON, &obj); err != nil {
				resp.Diagnostics.AddError("Unable to upgrade state", fmt.Sprintf("Decoding the prior state: %v", err))
				return
			}
			normalizeObject(obj, sr.Schema.Attributes, sr.Schema.Blocks)
			out, err := json.Marshal(obj)
			if err != nil {
				resp.Diagnostics.AddError("Unable to upgrade state", err.Error())
				return
			}
			resp.DynamicValue = &tfprotov6.DynamicValue{JSON: out}
		}},
	}
}

// normalizeObject rewrites obj in place to match attrs and blocks.
func normalizeObject(obj map[string]any, attrs map[string]schema.Attribute, blocks map[string]schema.Block) {
	for k := range obj {
		_, isAttr := attrs[k]
		_, isBlock := blocks[k]
		if !isAttr && !isBlock {
			delete(obj, k)
		}
	}
	for k, a := range attrs {
		v, ok := obj[k]
		if !ok {
			obj[k] = nil
			continue
		}
		// The SDK also stores unset optional collections as empty ones.
		if !a.IsRequired() && isEmptyCollection(v) {
			obj[k] = nil
			continue
		}
		switch a := a.(type) {
		case schema.StringAttribute:
			if s, isString := v.(string); isString && s == "" && !a.Required {
				obj[k] = nil
			}
		case schema.ListNestedAttribute:
			normalizeElements(v, a.NestedObject.Attributes, nil)
		case schema.SetNestedAttribute:
			normalizeElements(v, a.NestedObject.Attributes, nil)
		case schema.SingleNestedAttribute:
			if m, isObj := v.(map[string]any); isObj {
				normalizeObject(m, a.Attributes, nil)
			}
		}
	}
	for k, b := range blocks {
		v, ok := obj[k]
		if !ok {
			obj[k] = []any{}
			continue
		}
		switch b := b.(type) {
		case schema.ListNestedBlock:
			normalizeElements(v, b.NestedObject.Attributes, b.NestedObject.Blocks)
		case schema.SetNestedBlock:
			normalizeElements(v, b.NestedObject.Attributes, b.NestedObject.Blocks)
		}
	}
}

func normalizeElements(v any, attrs map[string]schema.Attribute, blocks map[string]schema.Block) {
	list, ok := v.([]any)
	if !ok {
		return
	}
	for _, e := range list {
		if m, isObj := e.(map[string]any); isObj {
			normalizeObject(m, attrs, blocks)
		}
	}
}

func isEmptyCollection(v any) bool {
	switch c := v.(type) {
	case []any:
		return len(c) == 0
	case map[string]any:
		return len(c) == 0
	}
	return false
}
