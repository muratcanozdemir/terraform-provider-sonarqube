variable "key" {
  description = "Project key. Changing it renames the key in place and keeps the analysis history."
  type        = string
}

variable "name" {
  description = "Display name of the project."
  type        = string
}

variable "visibility" {
  description = "`public` or `private`."
  type        = string
  default     = "private"
}

variable "tags" {
  description = "Project tags, e.g. the owning team. Portfolios can select projects by tag."
  type        = set(string)
  default     = []
}

variable "main_branch" {
  description = "Name of the main branch, when it is not the server default (`main`)."
  type        = string
  default     = null
}

variable "exclusions" {
  description = "Files to exclude from analysis (sonar.exclusions)."
  type        = list(string)
  default     = []
}

variable "quality_gate" {
  description = "Quality gate of the project. Null keeps the default gate."
  type        = string
  default     = null
}

variable "quality_profiles" {
  description = "Quality profile name by language key, e.g. { java = \"Acme Java\" }. Languages not listed use their default profile."
  type        = map(string)
  default     = {}
}

variable "new_code" {
  description = "New code definition of the project. Null inherits the global one."
  type = object({
    type  = string           # PREVIOUS_VERSION, NUMBER_OF_DAYS or REFERENCE_BRANCH
    value = optional(string) # days, or the reference branch
  })
  default = null
}

variable "links" {
  description = "Project links shown in SonarQube, by name. Names cannot be homepage, ci, issue, scm or scm_dev: analyses set those."
  type        = map(string)
  default     = {}
}

variable "admin_groups" {
  description = "Groups that administer the project and triage its issues and hotspots."
  type        = set(string)
  default     = []
}

variable "member_groups" {
  description = "Groups that can browse the project and see its source code."
  type        = set(string)
  default     = []
}

variable "scan_users" {
  description = "Logins allowed to run analyses of the project with their own tokens, e.g. a CI service account."
  type        = set(string)
  default     = []
}

variable "analysis_token" {
  description = <<-EOT
    Issue a project analysis token for CI. SonarQube issues analysis tokens
    only to the caller, so the token belongs to the account Terraform runs as.
    It is replaced rotate_before_days before it expires, at the first apply in
    that window. Null issues no token.
  EOT
  type = object({
    expiration_days    = optional(number, 90)
    rotate_before_days = optional(number, 14)
  })
  default = null
}

variable "github" {
  description = "Bind the project to a GitHub repository for pull request decoration. Developer edition or above."
  type = object({
    alm_setting             = string
    repository              = string # owner/name
    monorepo                = optional(bool, false)
    summary_comment_enabled = optional(bool, true)
  })
  default = null
}
