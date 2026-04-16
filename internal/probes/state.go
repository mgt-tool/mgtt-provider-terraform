package probes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mgt-tool/mgtt/sdk/provider"
)

func registerState(r *provider.Registry) {
	// state is a singleton-per-workdir resource; req.Name is conventional
	// (e.g. "main", "infra") but ignored by the probes — the workdir is
	// the real identity.
	r.Register("state", map[string]provider.ProbeFn{
		"accessible": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			tf, err := newFromReq(req)
			if err != nil {
				return provider.Result{}, err
			}
			if _, err := tf.StateList(ctx); err != nil {
				// NotFound / Forbidden / Transient all reduce to "cannot
				// observe" from the caller's perspective — return false
				// rather than surfacing a hard error so model flows
				// don't break on partial auth.
				if errors.Is(err, provider.ErrNotFound) ||
					errors.Is(err, provider.ErrForbidden) ||
					errors.Is(err, provider.ErrTransient) {
					return provider.BoolResult(false), nil
				}
				return provider.Result{}, err
			}
			return provider.BoolResult(true), nil
		},

		"locked": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			tf, err := newFromReq(req)
			if err != nil {
				return provider.Result{}, err
			}
			// Zero-timeout plan with refresh off. Normal exit = we got
			// the lock (not locked). `state lock` stderr classified as
			// ErrTransient = locked. Other classifications propagate
			// so the operator sees what's actually wrong.
			_, _, err = tf.Run(ctx, "plan", "-lock-timeout=0s", "-refresh=false",
				"-detailed-exitcode", "-input=false", "-no-color")
			if err == nil {
				return provider.BoolResult(false), nil
			}
			if errors.Is(err, provider.ErrTransient) && strings.Contains(err.Error(), "state lock") {
				return provider.BoolResult(true), nil
			}
			return provider.Result{}, err
		},

		"resource_count": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			tf, err := newFromReq(req)
			if err != nil {
				return provider.Result{}, err
			}
			addrs, err := tf.StateList(ctx)
			if err != nil {
				return provider.Result{}, err
			}
			return provider.IntResult(len(addrs)), nil
		},

		"terraform_version": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			tf, err := newFromReq(req)
			if err != nil {
				return provider.Result{}, err
			}
			out, _, err := tf.Run(ctx, "show", "-json", "-no-color")
			if err != nil {
				return provider.Result{}, err
			}
			// `terraform show -json` emits `terraform_version` as the
			// writer's version — the CLI that last serialized this state
			// file, not necessarily the current CLI. Useful for spotting
			// "someone ran a newer terraform and bumped us."
			var meta struct {
				TerraformVersion string `json:"terraform_version"`
			}
			if err := json.Unmarshal(out, &meta); err != nil {
				return provider.Result{}, fmt.Errorf("%w: parse show json: %v", provider.ErrProtocol, err)
			}
			return provider.StringResult(meta.TerraformVersion), nil
		},
	})
}
