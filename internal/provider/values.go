package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// strOrNull maps "" to null, for computed or optional attributes the server
// reports as absent with an empty string.
func strOrNull(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// keepNullIfEmpty returns null when the server reports "" and the prior value
// was null, so an omitted optional attribute does not show a perpetual diff,
// while a configured "" stays "".
func keepNullIfEmpty(prior types.String, s string) types.String {
	if s == "" && prior.IsNull() {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func stringSet(ss []string) types.Set {
	vals := make([]attr.Value, 0, len(ss))
	for _, s := range ss {
		vals = append(vals, types.StringValue(s))
	}
	return types.SetValueMust(types.StringType, vals)
}

func stringList(ss []string) types.List {
	vals := make([]attr.Value, 0, len(ss))
	for _, s := range ss {
		vals = append(vals, types.StringValue(s))
	}
	return types.ListValueMust(types.StringType, vals)
}

// stringsOf extracts the elements of a string set or list. Null and unknown
// collections yield nil.
func stringsOf(ctx context.Context, v interface {
	IsNull() bool
	IsUnknown() bool
	ElementsAs(context.Context, any, bool) diag.Diagnostics
}, diags *diag.Diagnostics) []string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(v.ElementsAs(ctx, &out, false)...)
	return out
}

// diffStrings returns the elements to add (in want, not in have) and to
// remove (in have, not in want).
func diffStrings(have, want []string) (add, remove []string) {
	for _, w := range want {
		if !slices.Contains(have, w) {
			add = append(add, w)
		}
	}
	for _, h := range have {
		if !slices.Contains(want, h) {
			remove = append(remove, h)
		}
	}
	return add, remove
}

// sameElements reports whether a and b hold the same strings, ignoring order.
func sameElements(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// ---------------------------------------------------------------------------
// Validators

type versionStringValidator struct{}

func (versionStringValidator) Description(context.Context) string {
	return "must be a dotted version such as 2026.1 or 26.11.0"
}
func (v versionStringValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

var versionRe = regexp.MustCompile(`^\d+(\.\d+)*$`)

func (v versionStringValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if !versionRe.MatchString(req.ConfigValue.ValueString()) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid version", v.Description(ctx))
	}
}

type durationValidator struct{}

func (durationValidator) Description(context.Context) string {
	return "must be a positive Go duration such as 30s or 2m"
}
func (v durationValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v durationValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	d, err := time.ParseDuration(req.ConfigValue.ValueString())
	if err != nil || d <= 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid duration", v.Description(ctx))
	}
}

// projectKeyRe is SonarQube's component key rule: letters, digits, '-', '_',
// '.', ':' and at least one non-digit.
var projectKeyRe = regexp.MustCompile(`^[\w\-.:]*[a-zA-Z\-_.:][\w\-.:]*$`)

type projectKeyValidator struct{}

func (projectKeyValidator) Description(context.Context) string {
	return "must be at most 400 characters of letters, digits, '-', '_', '.' or ':', with at least one non-digit"
}
func (v projectKeyValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v projectKeyValidator) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	if len(s) > 400 || !projectKeyRe.MatchString(s) {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid key", fmt.Sprintf("%q %s", s, v.Description(ctx)))
	}
}

// noSurroundingSpace rejects values SonarQube would silently trim, which
// would otherwise show as a permanent diff.
type noSurroundingSpace struct{}

func (noSurroundingSpace) Description(context.Context) string {
	return "must not start or end with whitespace"
}
func (v noSurroundingSpace) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v noSurroundingSpace) ValidateString(ctx context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	s := req.ConfigValue.ValueString()
	if strings.TrimSpace(s) != s {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", fmt.Sprintf("%q %s: SonarQube trims it, which would cause a permanent diff", s, v.Description(ctx)))
	}
}

func regexpMust(s string) *regexp.Regexp { return regexp.MustCompile(s) }

// randSuffix returns 12 random hex characters for generated names.
func randSuffix() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
