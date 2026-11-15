variable "release" {
  description = "What is being released: the SonarQube project key of each component, and the branch built for the release."
  type = object({
    version    = string
    components = set(string)
    branch     = optional(string) # null: the main branch
  })
}

variable "minimum_coverage" {
  description = "Overall coverage (%) each component must reach, on top of its quality gate."
  type        = number
  default     = 0
}

variable "minimum_server_version" {
  description = "Oldest SonarQube version the pipeline supports, as major.minor."
  type        = string
  default     = "9.9"
}
