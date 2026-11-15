# Re-apply the template to existing projects whenever its permissions change:
# terraform apply -invoke=action.sonarqube_apply_permission_template.internal
# runs it on demand.
action "sonarqube_apply_permission_template" "internal" {
  config {
    template = sonarqube_permission_template.internal.name
    search   = "internal-"
  }
}

resource "terraform_data" "internal_template" {
  input = [sonarqube_permissions.internal_developers.permissions]

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.sonarqube_apply_permission_template.internal]
    }
  }
}
