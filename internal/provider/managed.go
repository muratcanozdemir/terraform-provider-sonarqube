package provider

import (
	"context"
	"strings"
	"sync"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"

	"github.com/muratcanozdemir/sonarqube-go-client/sonarqube"
)

// An instance is "externally managed" when GitHub, GitLab or SCIM
// AUTO_PROVISIONING is enabled. SonarQube then synchronizes users, groups and
// group memberships from the identity provider and refuses to create or
// change them, with HTTP 400 "Operation not allowed when the instance is
// externally managed". Observed on SonarQube 2026.5 with GitHub
// AUTO_PROVISIONING:
//
//   - refused: creating users (local or not), changing a user's login, name,
//     email, local flag, SCM accounts or external identity; creating or
//     changing groups; adding or removing group members.
//   - allowed: changing a local user's password, deactivating and
//     anonymizing users, deleting groups, and every permission and
//     permission template operation.
//
// Resources check this at plan time, so that a change SonarQube will refuse
// fails the plan rather than half-way through an apply.

const managedMessage = "externally managed"

// isExternallyManaged reports whether err is SonarQube refusing an operation
// because users and groups are provisioned externally.
func isExternallyManaged(err error) bool {
	return sonarqube.StatusCode(err) == 400 && hasMessage(err, managedMessage)
}

// managedCache detects once whether the instance is externally managed.
type managedCache struct {
	client *sonarqube.Client

	mu      sync.Mutex
	known   bool
	managed bool
}

// get asks for managed groups only, a filter SonarQube accepts on managed
// instances alone: it answers 400 "The 'managed' parameter is only available
// for managed instances" otherwise. A server without the v2 API (404) is not
// managed. Other errors are returned and not cached.
func (c *managedCache) get(ctx context.Context) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.known {
		return c.managed, nil
	}
	one := 1
	_, _, err := c.client.Authorizations.SearchGroups(ctx, &sonarqube.AuthorizationsSearchGroupsOptions{Managed: new(true), PageSize: &one})
	switch {
	case err == nil:
		c.managed = true
	case sonarqube.StatusCode(err) == 400 && hasMessage(err, "only available for managed instances"),
		isUnknownURL(err):
		c.managed = false
	default:
		return false, err
	}
	c.known = true
	return c.managed, nil
}

// refuseIfManaged adds a plan error when the instance is externally managed.
// what names the change, e.g. "Creating group devs". A failed detection adds
// nothing: the apply then reports SonarQube's own error.
func (p *providerData) refuseIfManaged(ctx context.Context, diags *diag.Diagnostics, what string) {
	if p == nil || p.managed == nil {
		return
	}
	managed, err := p.managed.get(ctx)
	if err != nil || !managed {
		return
	}
	diags.AddError(what+" is not allowed: SonarQube is externally managed", managedDetail)
}

const managedDetail = "User provisioning (GitHub, GitLab or SCIM with AUTO_PROVISIONING) is enabled, so SonarQube synchronizes users, " +
	"groups and group memberships from the identity provider and refuses to create or change them. Manage them in the identity " +
	"provider instead. To stop managing an existing object with Terraform without destroying it, use a `removed` block with " +
	"`lifecycle { destroy = false }`. Permissions and permission templates can still be managed."

// planChange reports whether a plan creates the resource (create) or
// changes it in place or by replacement (update). Destroy plans and plans
// without a provider configuration report neither.
func planChange(r *resourceBase, req resource.ModifyPlanRequest) (create, update bool) {
	if !r.configured() || req.Plan.Raw.IsNull() {
		return false, false
	}
	if req.State.Raw.IsNull() {
		return true, false
	}
	return false, !req.Plan.Raw.Equal(req.State.Raw)
}

// managedHint is added to explain()'s detail for a refusal that was not
// caught at plan time, e.g. because provisioning was enabled in the same
// apply.
func managedHint(err error) string {
	if !isExternallyManaged(err) {
		return ""
	}
	return "\n\n" + strings.Replace(managedDetail, "User provisioning", "User provisioning was probably enabled", 1)
}
