# Lets a group edit one quality gate without global gate administration.
resource "sonarqube_qualitygate_usergroup_association" "platform" {
  gatename   = "Strict"
  group_name = "platform-team"
}
