# mgtt-provider-terraform

Terraform provider for [mgtt](https://github.com/mgt-tool/mgtt) — the model guided troubleshooting tool.

Reason across the **IaC / live-infra boundary**: mgtt already probes running components (kubernetes, aws, docker, …); this provider makes *Terraform state itself* a first-class component in the dependency graph.

## Why this matters

Most observability tools look at the live side and show you `aws_rds.available == false`. That's the *symptom*, and it sends you down a rabbit hole.

With this provider in the model, mgtt can tell you the real story:

```
-> probe aws_rds.available           ✗ false
-> probe tf_rds.exists_in_state      ✓ true
-> probe tf_rds.drifted              ✗ true    ← config and reality disagree
-> probe tf_state.last_applied_age     = 17 days
   root cause: tf_rds

   RDS is in the terraform state, but `terraform plan` would change it;
   last apply was 17 days ago. Someone changed RDS parameters out-of-band
   and the drift explains the outage.
```

Without the IaC layer, you'd waste an hour poking at AWS. With it, the root cause points you at `terraform apply` — or at whoever bypassed it.

The two components (`tf_rds` and `aws_rds`) describe the *same real-world database* from two different provider perspectives: Terraform's view ("is it in state, is it drifted?") and AWS's view ("is it live, is it reachable?"). Declaring both lets the engine reason across the boundary. See the naming convention discussion in the example below.

**The three failure classes this catches that live-only probing can't:**

1. **Drift** — config and reality disagree. Live side looks broken; `terraform apply` is the fix.
2. **Out-of-band deletion** — resource was removed from the state (or never created) but the model still references it.
3. **Stuck state lock** — every `terraform apply` is blocked because a stale lock is held; the whole IaC pipeline is frozen until someone breaks it.

## Types

| Type | Facts |
|---|---|
| `terraform.state` | `accessible`, `locked`, `resource_count`, `terraform_version` |
| `terraform.resource` | `exists_in_state`, `drifted`, `last_applied_age`, `dependents_count` |

## Install

Two equivalent paths — pick whichever fits your workflow:

```bash
# Git + host toolchain (requires Go 1.25+; warns if terraform not on PATH)
mgtt provider install terraform

# Pre-built Docker image (ships terraform inside; digest-pinned)
mgtt provider install --image ghcr.io/mgt-tool/mgtt-provider-terraform:0.1.0@sha256:...
```

The image is published by [this repo's CI](./.github/workflows/docker.yml) on every push to `main` and every `v*` tag. Find the current digest on the [GHCR package page](https://github.com/mgt-tool/mgtt-provider-terraform/pkgs/container/mgtt-provider-terraform).

## Capabilities

When installed as an image, this provider declares the following runtime capabilities in [`provider.yaml`](./provider.yaml) (top-level `needs:`):

| Capability | Effect at probe time |
|---|---|
| `terraform` | Mounts `$PWD` at `/workspace` and `-w /workspace` (the `.terraform/` plugin cache and state ride along); forwards `TF_CLI_CONFIG_FILE` and every `TF_VAR_*` set in the caller |
| `aws` | Mounts `~/.aws` read-only; forwards `AWS_PROFILE`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION`, `AWS_DEFAULT_REGION` (when set) — covers the AWS state backend and `aws` provider resources |

Plus `network: host` so the container reaches remote state backends (S3, GCS, Azure Storage, Terraform Cloud) and the cloud provider APIs.

If your state backend is GCP or Azure, add `gcloud` or `azure` to `needs` in this provider's `provider.yaml`; those caps forward `~/.config/gcloud` / `~/.azure` and the matching env chain.

Operators can override or extend the vocabulary via `$MGTT_HOME/capabilities.yaml`, and refuse specific caps via `MGTT_IMAGE_CAPS_DENY=...`. See the [full capabilities reference](https://github.com/mgt-tool/mgtt/blob/main/docs/reference/image-capabilities.md). Git-installed invocations don't go through this layer — the binary runs with the operator's full environment.

## Auth

This provider uses your existing terraform auth chain (`TF_VAR_*`, backend-specific credentials like `AWS_*`/`GOOGLE_*`/`ARM_*`, locked module versions in `.terraform/`). Workspace scoping is applied via the `TF_WORKSPACE` env var on each child invocation — the provider does NOT mutate `.terraform/environment`, so concurrent human `terraform` runs in the same workdir are not disturbed.

**Honest access declaration:** the `drifted` fact runs `terraform plan`, which refreshes state — that's a *write* to the state backend (state file). Other facts are pure reads. Operators who need strict read-only scope should:

- Bind a credential that cannot write to the state backend, AND
- Omit the `drifted` fact from their model.

The provider's `provider.yaml` declares `read_only: false` with a `writes_note:` describing this behavior. `mgtt provider install` prints the note at install time, and **`mgtt provider validate terraform` emits one yellow WARN line** about the non-read-only posture — intentional, it's asking you to confirm the credentials match the declared write scope.

## Timeouts

`drifted` runs `terraform plan -target=…` which can take 15–45 seconds against cloud backends on cold runs. Core's default probe timeout is 30s. If you see `ErrTransient: runner ... exceeded 30s` on the drift fact, raise it:

```bash
export MGTT_PROBE_TIMEOUT=120s
mgtt plan --component ingress
```

Other facts (`accessible`, `exists_in_state`, `resource_count`) run in well under a second.

## Example: cross-layer `system.model.yaml`

See [`examples/payments-api.model.yaml`](./examples/payments-api.model.yaml). The shape:

```yaml
meta:
  name: payments-api
  version: "1.2"
  providers: [terraform, kubernetes, aws]
  vars:
    tf_workdir: ./infrastructure

components:
  # IaC layer — "does Terraform think this exists, and does it match reality?"
  # Prefix `tf_` makes it unambiguous that this component is observed via
  # the terraform provider (type: terraform.*), not via AWS.
  tf_state:
    type: terraform.state
    vars: {workdir: "{tf_workdir}"}

  tf_rds:
    type: terraform.resource
    vars:
      workdir: "{tf_workdir}"
      # `address` is the Terraform *resource address* — the `<type>.<name>`
      # identifier Terraform itself uses. It's what `terraform state list`
      # prints, and matches the HCL declaration:
      #
      #     resource "aws_db_instance" "main" { ... }
      #       → terraform state list: aws_db_instance.main
      #
      # For resources inside a module: `module.infra.aws_db_instance.main`.
      address: aws_db_instance.main
    depends_on: [tf_state]

  # Live infra layer — same real-world RDS instance as tf_rds above,
  # observed via the aws provider. Prefix `aws_` makes the observation
  # boundary explicit so it's never ambiguous which provider reports
  # which fact.
  aws_rds:
    type: aws.rds_instance
    vars: {identifier: payments-prod}
    depends_on: [tf_rds]   # ← if TF doesn't know about it, observing live is moot

  api:
    type: kubernetes.deployment
    vars: {name: payments-api}
    depends_on: [aws_rds]

  ingress:
    type: kubernetes.ingress
    vars: {name: payments}
    entry_point: true
    depends_on: [api]
```

The engine walks the dependency chain upstream from the entry point: ingress → api → aws_rds → tf_rds → tf_state. Each layer adds information the next cannot produce on its own. The component *name* is just a label in the graph — the `type:` field tells you which provider does the observing, and the `tf_*` / `aws_*` prefix convention keeps the two views of the same real-world resource from blurring together.

## Architecture

The provider is a thin wiring layer on top of the mgtt SDK:

- `main.go` — 14 lines: registers types and calls `provider.Main`.
- `internal/probes/` — one file per type. Each registers ProbeFns against a `provider.Registry`.
- `internal/tfclassify/` — the **only** place terraform stderr phrasing (`Error acquiring the state lock`, `AccessDenied`, `NoCredentialProviders`, etc.) is recognized; maps those to the SDK's sentinel errors.

Plumbing (argv parsing, timeouts, size caps, `status:not_found` translation, exit codes, `version` subcommand, debug tracing) comes from the SDK.

## Development

```bash
go build .                    # compile locally
go test -race ./...           # unit tests
mgtt provider validate terraform     # static correctness
```
