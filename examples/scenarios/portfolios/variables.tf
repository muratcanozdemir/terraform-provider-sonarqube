variable "prefix" {
  description = "Prefix of every key."
  type        = string
  default     = "acme"
}

variable "projects" {
  description = "Projects by key, with the business unit and product they belong to."
  type = map(object({
    name    = string
    unit    = string # business unit: one portfolio each, selected by tag
    product = string # product: one application each
  }))
}
