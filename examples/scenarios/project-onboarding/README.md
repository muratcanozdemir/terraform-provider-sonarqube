# Scenario: onboard projects from a catalogue

Teams and their projects live in [`catalog.yaml`](catalog.yaml). One apply
creates, for each team, a member group and a leads group, and for each project
everything in [`../modules/project`](../modules/project): permissions, the
company quality gate, the new code definition, links, and a CI analysis token
that is rotated before it expires. SonarQube issues analysis tokens only to the
caller, so the tokens belong to the account Terraform runs as.

```sh
export SONAR_HOST=https://sonarqube.example.com SONAR_TOKEN=...   # an admin token
terraform init
terraform apply
terraform output -json analysis_tokens   # hand these to your CI system
```

Works on every edition.

To adopt projects that already exist, see [`../bulk-import`](../bulk-import).
