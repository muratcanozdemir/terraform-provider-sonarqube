action "sonarqube_send_test_email" "check" {
  config {
    to = "sonarqube-admins@example.com"
  }
}

# Fail the apply when the new SMTP settings do not work.
resource "sonarqube_email_configuration" "this" {
  host                   = "smtp.example.com"
  port                   = 587
  security_protocol      = "STARTTLS"
  from_address           = "sonarqube@example.com"
  username               = "sonarqube"
  basic_password_wo      = var.smtp_password
  credentials_wo_version = "1"

  lifecycle {
    action_trigger {
      events  = [after_create, after_update]
      actions = [action.sonarqube_send_test_email.check]
    }
  }
}
