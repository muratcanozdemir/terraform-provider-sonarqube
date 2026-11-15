variable "smtp_password" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "sonarqube_email_configuration" "this" {
  host              = "smtp.example.com"
  port              = 587
  security_protocol = "STARTTLS"
  from_address      = "sonarqube@example.com"
  subject_prefix    = "[SonarQube]"
  username          = "sonarqube"

  # Never stored in state. Bump the version to send a new password.
  basic_password_wo      = var.smtp_password
  credentials_wo_version = "1"
}
