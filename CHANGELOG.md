# Changelog

## [1.1.1](https://github.com/muratcanozdemir/terraform-provider-sonarqube/compare/v1.1.0...v1.1.1) (2026-11-24)


### Bug Fixes

* edge cases of quality gates, quality profiles and portfolios ([#17](https://github.com/muratcanozdemir/terraform-provider-sonarqube/issues/17)) ([372327b](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/372327b2afdc71aa2b7b82174ca735766dd3ba5e))
* handle externally managed instances and edge cases in paging, settings and errors ([#15](https://github.com/muratcanozdemir/terraform-provider-sonarqube/issues/15)) ([6fa4dcd](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/6fa4dcd8d4e6ce497e6639b5814c14b75c3e49a6))

## [1.1.0](https://github.com/muratcanozdemir/terraform-provider-sonarqube/compare/v1.0.1...v1.1.0) (2026-11-15)


### Features

* token rotation, branch retention, GitHub/GitLab/SCIM provisioning ([#11](https://github.com/muratcanozdemir/terraform-provider-sonarqube/issues/11)) ([821bff0](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/821bff02a7e330a0edea5139e9c1c30de576a405))


### Bug Fixes

* **deps:** use sonarqube-go-client 1.0.3 and drop the update-field workaround ([#10](https://github.com/muratcanozdemir/terraform-provider-sonarqube/issues/10)) ([8712916](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/87129166654bf7ab2b76aa8c66d3cd4a12bfb351))
* scm_accounts updates, plugin lifecycle and secured keys in settings ([c253287](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/c25328785e02ec9fa2c80e46579d86500247d997))

## [1.0.1](https://github.com/muratcanozdemir/terraform-provider-sonarqube/compare/v1.0.0...v1.0.1) (2026-11-15)


### Bug Fixes

* 1) Avoid crash when provider settings are unknown at plan time 2) Empty sonarqube_settings ([f278173](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/f278173494ab4fc3dda612b7d0e6b38d6cd86379))
* Two fixes for QoL ([2173aa6](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/2173aa646a746aea6dbc199d6e121e3d6bca9cde))

## 1.0.0 (2026-11-15)


### Features

* first commit ([093f484](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/093f484d14ef224b81e5fac15826284c15c3886c))


### Bug Fixes

* fixing govulnchecks ([f54aaa7](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/f54aaa72ce0a01e8b2d35a73f41d170dffc16212))
* fixing govulnchecks ([163d41e](https://github.com/muratcanozdemir/terraform-provider-sonarqube/commit/163d41e0e86205a952dfeb9cfeeef5478d149832))
