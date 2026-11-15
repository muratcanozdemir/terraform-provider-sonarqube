variable "prefix" {
  description = "Prefix of project and setting keys."
  type        = string
  default     = "acme"
}

variable "github_app" {
  description = "The GitHub App SonarQube uses to decorate pull requests and to log users in."
  type = object({
    app_id    = string
    client_id = string
    api_url   = optional(string, "https://api.github.com/")
    web_url   = optional(string, "https://github.com/")
  })
}

variable "github_app_client_secret" {
  description = "Client secret of the GitHub App. Write-only: never stored in state."
  type        = string
  sensitive   = true
  ephemeral   = true
}

variable "github_app_private_key" {
  description = "PEM private key of the GitHub App. Write-only: never stored in state."
  type        = string
  sensitive   = true
  ephemeral   = true
}

variable "credentials_version" {
  description = "Change to send new values of the GitHub App secrets, e.g. after rotating the private key."
  type        = string
  default     = "1"
}

variable "github_login" {
  description = "Let members of these GitHub organizations log in to SonarQube. Null leaves GitHub authentication unmanaged."
  type = object({
    enabled            = optional(bool, true)
    organizations      = set(string)
    synchronize_groups = optional(bool, true)
  })
  default = null
}

variable "repositories" {
  description = <<-EOT
    SonarQube projects by key, each bound to a GitHub repository. Several
    projects in one repository make it a monorepo.
  EOT
  type = map(object({
    name       = string
    repository = string # owner/name
  }))
}
