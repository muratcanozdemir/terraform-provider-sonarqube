variable "prefix" {
  description = "Prefix of group, gate and template names."
  type        = string
  default     = "acme"
}

variable "make_defaults" {
  description = "Make the gate and permission template the server defaults. Turn off to try the baseline next to an existing setup."
  type        = bool
  default     = true
}

variable "smtp" {
  description = "Outgoing mail server. Null leaves email unconfigured."
  type = object({
    host     = string
    port     = optional(number, 587)
    username = string
    from     = string
  })
  default = null
}

variable "smtp_password" {
  description = "SMTP password. Write-only: never stored in state."
  type        = string
  default     = null
  sensitive   = true
  ephemeral   = true
}

variable "webhook_url" {
  description = "Receives every project's analysis results, e.g. a chat or deployment bot. Null creates no webhook."
  type        = string
  default     = null
}

variable "webhook_secret" {
  description = "HMAC secret signing webhook payloads (16-200 characters), so receivers can reject forged calls. Write-only: never stored in state."
  type        = string
  default     = null
  sensitive   = true
  ephemeral   = true

  validation {
    condition     = var.webhook_url == null || var.webhook_secret != null
    error_message = "Set webhook_secret with webhook_url: unsigned webhooks cannot be told apart from forged ones."
  }
}

variable "secrets_version" {
  description = "Change to send new values of smtp_password and webhook_secret."
  type        = string
  default     = "1"
}
