# A token that exists only for the duration of one Terraform run, for example
# to configure another provider. It is never written to state or plan files.
ephemeral "sonarqube_user_token" "run" {
  name_prefix = "terraform-run"
  type        = "USER_TOKEN"
}
