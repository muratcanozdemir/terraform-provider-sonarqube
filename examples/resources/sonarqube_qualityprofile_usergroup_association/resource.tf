# Lets a group edit one quality profile without global profile administration.
resource "sonarqube_qualityprofile_usergroup_association" "platform" {
  language     = "java"
  profile_name = "Company Java"
  group_name   = "platform-team"
}
