---
page_title: "Migrating from jdamata/sonarqube"
subcategory: ""
description: |-
  Move existing configurations and state from the jdamata/sonarqube provider without recreating anything.
---

# Migrating from jdamata/sonarqube

This provider uses the same resource and data source names and arguments as
[jdamata/sonarqube](https://registry.terraform.io/providers/jdamata/sonarqube),
so existing state carries over: switching changes the provider, not the
SonarQube objects. On first use the provider upgrades the old state to its
own format.

The path below is tested end to end on every change: a configuration with
every resource type is applied with jdamata/sonarqube 0.16.29, migrated, and
must then plan with no changes (`scripts/migration-test.sh`).

## Steps

1. **Start from a clean plan on jdamata/sonarqube 0.16.29.** Upgrade to that
   release first if you are on an older one, and make sure `terraform plan`
   shows no changes, so that anything the migration shows is caused by the
   migration.

2. **Back up the state.**

   ```shell
   terraform state pull > pre-migration.tfstate
   ```

3. **Change the provider source** in `required_providers`, and make the
   configuration changes listed below if they apply to you.

   ```terraform
   terraform {
     required_providers {
       sonarqube = {
         source = "muratcanozdemir/sonarqube"
       }
     }
   }
   ```

   The `provider "sonarqube"` block needs no change: every argument of
   jdamata/sonarqube is supported with the same meaning.

4. **Point the state at the new provider**, then install it.

   ```shell
   terraform state replace-provider registry.terraform.io/jdamata/sonarqube registry.terraform.io/muratcanozdemir/sonarqube
   terraform init -upgrade
   ```

5. **Plan.** Expect no changes, except the ones listed under
   [Expected plan changes](#expected-plan-changes). Nothing should be
   created, replaced or destroyed; if something is, stop and restore the
   backup with `terraform state push pre-migration.tfstate` after switching
   the source back.

## Configuration changes

- **`sonarqube_portfolio.selected_projects`** is an attribute, not a block:

  ```terraform
  # jdamata/sonarqube
  selected_projects {
    project_key = "my-project"
  }

  # muratcanozdemir/sonarqube
  selected_projects = [
    { project_key = "my-project" },
  ]
  ```

- **`sonarqube_qualitygate_project_association.gatename`** is required. Gate
  IDs no longer exist in current SonarQube releases; a configuration that only
  sets `gateid` must set `gatename` instead.

## Expected plan changes

- **`sonarqube_alm_github` with a `url` ending in `/`**
  (`https://api.github.com/`): SonarQube stores the URL without the slash, and
  jdamata/sonarqube recorded that form. The first plan shows a one-time
  in-place update of `url`; applying it changes nothing on the server. (jdamata/sonarqube
  plans a replacement of the setting for the same configuration.)

## What changes in behavior

These are deliberate, and none needs a configuration change:

- **Destructive operations that would cascade are refused.** Destroying a
  quality profile that still has child profiles, or a DevOps platform
  setting that projects outside Terraform are bound to, fails with an
  explanation instead of deleting more than the configuration describes.
- **Edition checks happen at plan time.** Applications and bindings need the
  Developer edition, portfolios the Enterprise edition; on a smaller edition
  the plan fails before anything is changed.
- **Quality profile changes are serialized** within a run, because SonarQube
  deadlocks on concurrent changes to a profile hierarchy.
- **`sonarqube_project.tags`** is a set: order no longer matters.
- **Users are deactivated on destroy**, as before; set
  `anonymize_user_on_delete` to also anonymize them.

## New in this provider

- Write-only arguments that keep secrets out of state (Terraform 1.11+):
  `password_wo` on `sonarqube_user`, `value_wo` on `sonarqube_secured_setting`,
  `secret_wo` on `sonarqube_webhook`, and `*_wo` forms of every DevOps
  platform credential (`personal_access_token_wo`, `client_secret_wo`,
  `private_key_wo`, `webhook_secret_wo`) with `credentials_wo_version`.
  To move a secret out of state after migrating, replace the argument with
  its `_wo` form and set the version.
- An ephemeral `sonarqube_user_token` (Terraform 1.10+), and `metadata_only`
  on the `sonarqube_user_token` resource to track and revoke a token without
  storing its value.
- `component` on `sonarqube_setting`, for project, application and portfolio
  settings.
- Bitbucket Cloud: `sonarqube_alm_bitbucket_cloud` and
  `sonarqube_bitbucket_cloud_binding`.
- `retrieve_badge_token` on `sonarqube_project`, `restart_required` on
  `sonarqube_plugin`, `uuid` and `scm_accounts` on `sonarqube_user`, and the
  `sonarqube_server` data source.
