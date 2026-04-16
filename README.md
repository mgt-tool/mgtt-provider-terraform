# mgtt-provider-terraform

Terraform provider for [mgtt](https://github.com/mgt-tool/mgtt) — the model guided troubleshooting tool.

Reason across the **IaC / live-infra boundary**: mgtt already probes running components (kubernetes, aws, docker, …); this provider makes *Terraform state itself* a first-class component in the dependency graph.

## Why this matters

Most observability tools look at the live side and show you `rds_main.available == false`. That's the *symptom*, and it sends you down a rabbit hole.

With this provider in the model, mgtt can tell you the real story:

```
-> probe rds_main.available          ✗ false
-> probe tf_rds.exists_in_state      ✓ true
-> probe tf_rds.drifted              ✗ true    ← config and reality disagree
-> probe tf_state.last_applied_age     = 17 days
   root cause: tf_rds

   rds_main is in the state, but terraform plan would change it;
   last apply was 17 days ago. Someone changed RDS parameters out-of-band
   and the drift explains the outage.
```

Without the IaC layer, you'd waste an hour poking at AWS. With it, the root cause points you at `terraform apply` — or at whoever bypassed it.

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

```bash
mgtt provider install terraform
```

Install hook gates on Go 1.21+ and warns if `terraform` is not yet on PATH at install time (it's needed at probe time, not build time).

## Auth

This provider uses your existing terraform auth chain (`TF_VAR_*`, backend-specific credentials like `AWS_*`/`GOOGLE_*`/`ARM_*`, locked module versions in `.terraform/`). Workspace scoping is applied via the `TF_WORKSPACE` env var on each child invocation — the provider does NOT mutate `.terraform/environment`, so concurrent human `terraform` runs in the same workdir are not disturbed.

**Honest access declaration:** the `drifted` fact runs `terraform plan`, which refreshes state — that's a *write* to the state backend (state file). Other facts are pure reads. Operators who need strict read-only scope should:

- Bind a credential that cannot write to the state backend, AND
- Omit the `drifted` fact from their model.

The provider's `auth.access.writes: state-refresh-on-plan` declares this explicitly so operators aren't surprised. **Expect `mgtt provider validate terraform` to emit one yellow WARN line** about the non-`none` writes declaration — that's intentional, it's asking you to confirm the credentials match this scope.

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
    namespace: production
    tf_workdir: ./infrastructure

components:
  tf_state:
    type: terraform.state
    vars: {workdir: "{tf_workdir}"}

  rds_main:
    type: terraform.resource
    vars: {workdir: "{tf_workdir}", address: aws_db_instance.main}
    depends_on: [tf_state]

  rds_live:
    type: aws.rds_instance
    vars: {identifier: payments-prod}
    depends_on: [rds_main]     # ← if TF doesn't know about it, observing live is moot

  api:
    type: kubernetes.deployment
    vars: {name: payments-api}
    depends_on: [rds_live]

  ingress:
    type: kubernetes.ingress
    vars: {name: payments}
    entry_point: true
    depends_on: [api]
```

The engine walks left-to-right: ingress → api → rds_live → rds_main → tf_state. Each layer adds information the layer to its right cannot produce on its own.

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
