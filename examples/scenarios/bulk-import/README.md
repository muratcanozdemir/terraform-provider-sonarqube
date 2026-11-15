# Scenario: adopt an existing server

Bring objects created in the UI or by another tool under Terraform, without
writing their configuration by hand. Needs Terraform 1.14+.

1. List what to adopt. [`adopt.tfquery.hcl`](adopt.tfquery.hcl) lists
   projects, groups and users matching `search`, plus every custom quality
   gate and profile:

   ```sh
   export SONAR_HOST=https://sonarqube.example.com SONAR_TOKEN=...
   terraform init
   terraform query -var search=payments
   ```

2. Generate configuration with an `import` block for each object:

   ```sh
   terraform query -var search=payments -generate-config-out=generated.tf
   ```

3. Review the plan. It should only **import**: anything else means the
   generated configuration does not match the server yet.

   ```sh
   terraform plan -var search=payments   # Plan: N to import, 0 to add, 0 to change, 0 to destroy.
   terraform apply -var search=payments
   ```

4. Tidy `generated.tf`: rename the resources, replace literal keys with
   references, move them into modules such as [`../modules/project`](../modules/project),
   and delete the `import` blocks once applied.

To adopt objects one at a time instead, every resource also supports
`import` blocks by ID or by
[identity](https://developer.hashicorp.com/terraform/language/import#identity),
see each resource's documentation.
