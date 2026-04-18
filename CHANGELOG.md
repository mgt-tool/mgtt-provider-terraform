# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). Versioning: [SemVer](https://semver.org/).

## [1.0.0] — 2026-04-18

### Changed (breaking)

- `manifest.yaml` migrated to the v1.0 mgtt schema: three top-level blocks (`meta`, `runtime`, `install`); `hooks:` retired; `needs:` + `network:` moved under `runtime:`; install methods declared via `install.source` + `install.image` subblocks. Requires mgtt ≥ 0.2.0.

## [0.1.0] — 2026-04-16

Initial release. mgtt observes Terraform-managed infrastructure at the IaC layer.

### Added

- **`terraform.state` type** with facts `accessible`, `locked`, `resource_count`, `terraform_version`.
- **`terraform.resource` type** with facts `exists_in_state`, `drifted`, `last_applied_age`, `dependents_count`.
- **`internal/tfclassify/`** — maps real-world terraform CLI stderr (state lock, backend auth, missing credentials, network outages) to the SDK's sentinel error taxonomy.
- **Cross-layer example** in `examples/payments-api.model.yaml` wiring terraform.* alongside kubernetes.* and aws.* for a three-layer model walk.
- **CI workflow** — lint + unit on every push.

### Honest limitations

- **`drifted` writes to state** because `terraform plan` refreshes. Operators who need strict read-only must scope their backend credential accordingly and omit the `drifted` fact from their model. `auth.access.writes: state-refresh-on-plan` makes this explicit.
- **`last_applied_age` is local-backend only.** Remote backends (S3, GCS, Terraform Cloud) don't expose a last-apply timestamp via the CLI. For remote backends, operators typically observe apply recency via CI pipeline metadata.
- **`dependents_count` is an approximation.** It counts quoted-address references in `terraform show -json`; precise dependency graph walking would require parsing `terraform graph`.
