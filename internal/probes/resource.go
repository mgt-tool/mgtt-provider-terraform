package probes

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mgt-tool/mgtt/sdk/provider"
)

func registerResource(r *provider.Registry) {
	r.Register("resource", map[string]provider.ProbeFn{
		"exists_in_state": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			addr, err := resolveAddress(req)
			if err != nil {
				return provider.Result{}, err
			}
			tf, err := newFromReq(req)
			if err != nil {
				return provider.Result{}, err
			}
			addrs, err := tf.StateList(ctx)
			if err != nil {
				return provider.Result{}, err
			}
			for _, a := range addrs {
				if a == addr {
					return provider.BoolResult(true), nil
				}
			}
			return provider.BoolResult(false), nil
		},

		"drifted": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			addr, err := resolveAddress(req)
			if err != nil {
				return provider.Result{}, err
			}
			tf, err := newFromReq(req)
			if err != nil {
				return provider.Result{}, err
			}
			// -detailed-exitcode: 0 no changes, 2 changes, 1 error.
			// Scope to just this resource so the plan stays cheap.
			_, code, err := tf.Run(ctx, "plan", "-detailed-exitcode",
				"-target="+addr, "-lock=false", "-input=false", "-no-color")
			if err != nil {
				return provider.Result{}, err
			}
			switch code {
			case 0:
				return provider.BoolResult(false), nil
			case 2:
				return provider.BoolResult(true), nil
			default:
				return provider.Result{}, fmt.Errorf("%w: unexpected plan exit %d", provider.ErrProtocol, code)
			}
		},

		"last_applied_age": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			workdir, err := ResolveWorkdir(req)
			if err != nil {
				return provider.Result{}, err
			}
			// Local backend, default state path only.
			return provider.IntResult(StateFileMtimeAge(workdir)), nil
		},

		"dependents_count": func(ctx context.Context, req provider.Request) (provider.Result, error) {
			addr, err := resolveAddress(req)
			if err != nil {
				return provider.Result{}, err
			}
			tf, err := newFromReq(req)
			if err != nil {
				return provider.Result{}, err
			}
			out, _, err := tf.Run(ctx, "show", "-json", "-no-color")
			if err != nil {
				return provider.Result{}, err
			}
			count, err := countDependents(out, addr)
			if err != nil {
				return provider.Result{}, err
			}
			return provider.IntResult(count), nil
		},
	})
}

// resolveAddress pulls the TF address from Extra["address"]. Returns
// ErrUsage if absent — the component key is NOT a TF address by convention.
func resolveAddress(req provider.Request) (string, error) {
	if a := req.Extra["address"]; a != "" {
		return a, nil
	}
	return "", fmt.Errorf("%w: terraform.resource requires --address <terraform-address>", provider.ErrUsage)
}

// showJSON matches the subset of `terraform show -json` we care about.
type showJSON struct {
	Values struct {
		RootModule moduleValues `json:"root_module"`
	} `json:"values"`
	Configuration struct {
		RootModule moduleConfig `json:"root_module"`
	} `json:"configuration"`
}

type moduleValues struct {
	Resources    []resourceValue `json:"resources"`
	ChildModules []moduleValues  `json:"child_modules"`
}

type resourceValue struct {
	Address   string   `json:"address"`
	DependsOn []string `json:"depends_on"`
}

type moduleConfig struct {
	Resources   map[string]resourceConfig `json:"resources"`
	ModuleCalls map[string]moduleCall     `json:"module_calls"`
}

type moduleCall struct {
	Module moduleConfig `json:"module"`
}

type resourceConfig struct {
	// expressions maps attribute name → expression tree. We walk it
	// generically because attribute names vary by resource type.
	Expressions map[string]json.RawMessage `json:"expressions"`
	DependsOn   []string                   `json:"depends_on"`
}

// countDependents walks `terraform show -json` structurally and counts
// distinct resources whose depends_on or expression.references include the
// target address. Walks child modules recursively. Parses the JSON properly
// rather than substring-scanning, so descriptions/tags that happen to
// contain the address don't produce false positives.
func countDependents(showBlob []byte, addr string) (int, error) {
	var root showJSON
	if err := json.Unmarshal(showBlob, &root); err != nil {
		return 0, fmt.Errorf("%w: parse terraform show json: %v", provider.ErrProtocol, err)
	}
	seen := map[string]bool{}
	walkValues(root.Values.RootModule, addr, seen)
	walkConfig(root.Configuration.RootModule, addr, seen)
	return len(seen), nil
}

func walkValues(m moduleValues, addr string, seen map[string]bool) {
	for _, r := range m.Resources {
		if r.Address == addr {
			continue // self
		}
		for _, dep := range r.DependsOn {
			if dep == addr {
				seen[r.Address] = true
				break
			}
		}
	}
	for _, child := range m.ChildModules {
		walkValues(child, addr, seen)
	}
}

func walkConfig(m moduleConfig, addr string, seen map[string]bool) {
	for localKey, r := range m.Resources {
		if localKey == addr {
			continue
		}
		for _, dep := range r.DependsOn {
			if dep == addr {
				seen[localKey] = true
				break
			}
		}
		// Expression-tree references: the `references` array inside each
		// expression names the resources this expression reads.
		for _, exprTree := range r.Expressions {
			if exprRefsAddress(exprTree, addr) {
				seen[localKey] = true
				break
			}
		}
	}
	for _, call := range m.ModuleCalls {
		walkConfig(call.Module, addr, seen)
	}
}

// exprRefsAddress checks whether any `references` array in an expression
// tree contains the target address. `references` is the structured,
// documented place terraform show puts dependency edges.
func exprRefsAddress(tree json.RawMessage, addr string) bool {
	var generic any
	if err := json.Unmarshal(tree, &generic); err != nil {
		return false
	}
	return searchRefs(generic, addr)
}

func searchRefs(node any, addr string) bool {
	switch v := node.(type) {
	case map[string]any:
		if refs, ok := v["references"].([]any); ok {
			for _, r := range refs {
				if s, ok := r.(string); ok && s == addr {
					return true
				}
			}
		}
		for _, child := range v {
			if searchRefs(child, addr) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if searchRefs(child, addr) {
				return true
			}
		}
	}
	return false
}
