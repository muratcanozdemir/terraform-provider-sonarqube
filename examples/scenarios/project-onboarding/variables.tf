variable "prefix" {
  description = "Prefix of every key and name, to keep several environments (or test runs) apart on one server."
  type        = string
  default     = "acme"
}

variable "catalog_file" {
  description = "YAML file listing teams and their projects."
  type        = string
  default     = "catalog.yaml"
}
