package provider

import (
	"context"
	"encoding/json"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// When a quality gate or profile is made the default, the one it replaces is
// remembered in the resource's private state, and made the default again
// when this one stops being it (is_default unset, or destroyed). Without a
// remembered one (an imported resource, or one already default), the
// built-in gate or profile is used.
//
// Guessing the built-in default is unreliable: SonarQube 2026 ships several
// built-in profiles per language ("Sonar way core", "... extended",
// "... comprehensive", "Sonar agentic AI"), still accepts "Sonar way" as an
// alias of "Sonar way comprehensive" in api/qualityprofiles/set_default,
// while a fresh install's default is "Sonar way core"; Enterprise also ships
// a second built-in gate.

const previousDefaultKey = "previous_default"

type privateReader interface {
	GetKey(ctx context.Context, key string) ([]byte, diag.Diagnostics)
}

type privateWriter interface {
	SetKey(ctx context.Context, key string, value []byte) diag.Diagnostics
}

// previousDefault returns the remembered default, or "".
func previousDefault(ctx context.Context, p privateReader) string {
	if p == nil {
		return ""
	}
	b, d := p.GetKey(ctx, previousDefaultKey)
	if d.HasError() || len(b) == 0 {
		return ""
	}
	var name string
	if json.Unmarshal(b, &name) != nil {
		return ""
	}
	return name
}

// rememberDefault records name as the default to restore; "" forgets it.
func rememberDefault(ctx context.Context, p privateWriter, name string) diag.Diagnostics {
	if p == nil {
		return nil
	}
	if name == "" {
		return p.SetKey(ctx, previousDefaultKey, nil)
	}
	b, _ := json.Marshal(name)
	return p.SetKey(ctx, previousDefaultKey, b)
}

// restoreTarget picks the default to restore when self stops being it: the
// remembered one if it still exists and is not self, else fallback().
func restoreTarget(remembered, self string, exists func(string) bool, fallback func() (string, error)) (string, error) {
	if remembered != "" && remembered != self && exists(remembered) {
		return remembered, nil
	}
	return fallback()
}
