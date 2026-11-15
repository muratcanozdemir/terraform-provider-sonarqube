variable "prefix" {
  description = "Prefix of profile, rule and group names."
  type        = string
  default     = "acme"
}

variable "make_default" {
  description = "Make the company Java profile the default for Java projects."
  type        = bool
  default     = false
}

variable "rule_overrides" {
  description = <<-EOT
    Rules to activate in the company profile on top of the inherited ones, by
    rule key. severity and params are optional; params is
    "key1=value1;key2=value2". Omitted fields keep the rule's defaults and are
    not tracked for drift.
  EOT
  type = map(object({
    severity = optional(string)
    params   = optional(string)
  }))
  default = {
    "java:S107"  = { params = "max=5;constructorMax=10" } # too many parameters
    "java:S3776" = { params = "Threshold=10" }            # cognitive complexity
    "java:S1135" = { severity = "INFO" }                  # TODO comments: track, don't nag
  }
}

variable "legacy_exemptions" {
  description = "Rules the legacy team's profile switches off, by rule key."
  type        = set(string)
  default     = ["java:S107"]
}
