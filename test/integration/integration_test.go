//go:build integration

// Package integration exercises the terraform provider end-to-end against a
// real `terraform` CLI. Fixtures use hashicorp/null so no cloud credentials
// or network are required beyond what `terraform init` needs to fetch the
// null provider plugin the first time.
//
// Run with:
//
//	go test -tags=integration ./test/integration/...
//
// Requirements on the host: `terraform` and `go` on $PATH. The tests use a
// temp copy of each fixture directory so concurrent runs do not collide and
// the committed testdata/*/main.tf files are never mutated.
package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Scenario harness
// ---------------------------------------------------------------------------

// fixture stages a fresh copy of testdata/<name>/ into a tempdir, runs
// `terraform init && terraform apply`, and registers a cleanup. Returns the
// staged workdir path.
func fixture(t *testing.T, name string) string {
	t.Helper()
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not on PATH; skipping integration test")
	}
	src := filepath.Join("testdata", name)
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("fixture %q not found: %v", name, err)
	}

	dst := t.TempDir()
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	runIn(t, dst, 5*time.Minute, "terraform", "init", "-input=false", "-no-color")
	runIn(t, dst, 3*time.Minute, "terraform", "apply", "-auto-approve", "-input=false", "-no-color")
	return dst
}

// runIn runs a command in dir with a timeout; failures fail the test with
// captured stderr so the reader can see exactly what terraform did.
func runIn(t *testing.T, dir string, timeout time.Duration, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v\nstdout: %s\nstderr: %s",
			name, strings.Join(args, " "), err, out, stderr.String())
	}
}

// buildProviderBinary compiles the runner with matching ldflags so the
// `version` subcommand returns something meaningful.
func buildProviderBinary(t *testing.T) string {
	t.Helper()
	root := repoRoot(t)
	bin := filepath.Join(t.TempDir(), "mgtt-provider-terraform")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build provider: %v\n%s", err, out)
	}
	return bin
}

// probe runs the provider binary and returns the parsed JSON response.
func probe(t *testing.T, binary, typ, name, fact string, extras ...string) probeResult {
	t.Helper()
	args := []string{"probe", name, fact, "--type", typ}
	args = append(args, extras...)
	cmd := exec.Command(binary, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("probe %s.%s name=%s extras=%v: %v\nstderr: %s",
			typ, fact, name, extras, err, stderr.String())
	}
	var r probeResult
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("decode probe output: %v (raw=%q)", err, out)
	}
	return r
}

type probeResult struct {
	Value  any    `json:"value"`
	Raw    string `json:"raw"`
	Status string `json:"status"`
}

// ---------------------------------------------------------------------------
// Scenario 1 — a healthy, freshly-applied state
// ---------------------------------------------------------------------------
//
// The baseline: everything TF thinks should exist does exist, no drift,
// lock is free. This is the shape of a just-merged `terraform apply`.

func TestScenario_HealthyState(t *testing.T) {
	workdir := fixture(t, "simple")
	binary := buildProviderBinary(t)

	t.Run("state.accessible is true", func(t *testing.T) {
		r := probe(t, binary, "state", "main", "accessible", "--workdir", workdir)
		if r.Value != true {
			t.Fatalf("want true, got %v (status=%s)", r.Value, r.Status)
		}
	})

	t.Run("state.resource_count matches fixture", func(t *testing.T) {
		r := probe(t, binary, "state", "main", "resource_count", "--workdir", workdir)
		// Fixture declares 3 null_resources.
		if v, _ := r.Value.(float64); int(v) != 3 {
			t.Fatalf("want 3, got %v", r.Value)
		}
	})

	t.Run("state.terraform_version is non-empty", func(t *testing.T) {
		r := probe(t, binary, "state", "main", "terraform_version", "--workdir", workdir)
		s, _ := r.Value.(string)
		if s == "" {
			t.Fatalf("want non-empty version, got %q", s)
		}
	})

	t.Run("resource.exists_in_state → true for declared address", func(t *testing.T) {
		r := probe(t, binary, "resource", "alpha", "exists_in_state",
			"--workdir", workdir, "--address", "null_resource.alpha")
		if r.Value != true {
			t.Fatalf("want true, got %v", r.Value)
		}
	})

	t.Run("resource.drifted → false right after apply", func(t *testing.T) {
		r := probe(t, binary, "resource", "alpha", "drifted",
			"--workdir", workdir, "--address", "null_resource.alpha")
		if r.Value != false {
			t.Fatalf("fresh apply should show no drift, got %v", r.Value)
		}
	})
}

// ---------------------------------------------------------------------------
// Scenario 2 — a Terraform address that is not in the state
// ---------------------------------------------------------------------------
//
// The operator misremembered the address, or a resource was removed via
// `terraform state rm` / manual deletion. exists_in_state must cleanly
// report false, NOT surface as a hard error. This is the symmetric twin of
// the k8s provider's "probe a missing deployment" scenario.

func TestScenario_ResourceNotInState(t *testing.T) {
	workdir := fixture(t, "simple")
	binary := buildProviderBinary(t)

	r := probe(t, binary, "resource", "phantom", "exists_in_state",
		"--workdir", workdir, "--address", "null_resource.does_not_exist")
	if r.Value != false {
		t.Fatalf("phantom address must yield false, got %v (status=%s)", r.Value, r.Status)
	}
}

// ---------------------------------------------------------------------------
// Scenario 3 — drift detection
// ---------------------------------------------------------------------------
//
// Apply the `drift` fixture with label=applied-v1. Then rewrite main.tf to
// label=rewritten-v2 WITHOUT re-applying. The next `terraform plan` must
// report pending changes for null_resource.drifty → drifted == true.
//
// This is the flagship use case — "config and reality disagree" — and the
// probe has to detect it without requiring the operator to pre-run plan.

func TestScenario_DriftDetected(t *testing.T) {
	workdir := fixture(t, "drift")
	binary := buildProviderBinary(t)

	// Sanity: right after apply, no drift.
	r := probe(t, binary, "resource", "drifty", "drifted",
		"--workdir", workdir, "--address", "null_resource.drifty")
	if r.Value != false {
		t.Fatalf("pre-edit: want false, got %v", r.Value)
	}

	// Rewrite main.tf in-place so config diverges from state.
	main := filepath.Join(workdir, "main.tf")
	contents, err := os.ReadFile(main)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(contents), "applied-v1", "rewritten-v2", 1)
	if edited == string(contents) {
		t.Fatal("edit did not take — fixture is stale?")
	}
	if err := os.WriteFile(main, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	// Probe again — drift should now be visible.
	r = probe(t, binary, "resource", "drifty", "drifted",
		"--workdir", workdir, "--address", "null_resource.drifty")
	if r.Value != true {
		t.Fatalf("post-edit: want true (drift visible), got %v", r.Value)
	}
}

// ---------------------------------------------------------------------------
// Scenario 4 — state backend is unreachable
// ---------------------------------------------------------------------------
//
// Point the provider at a workdir that is NOT a Terraform root. state.accessible
// must report false cleanly (no terraform state → NotFound classified by
// tfclassify → BoolResult(false) in the probe). Matches the k8s provider's
// "probe against a missing cluster" symmetric.

func TestScenario_StateBackendAbsent(t *testing.T) {
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Skip("terraform not on PATH")
	}
	empty := t.TempDir() // no .tf files, no state
	binary := buildProviderBinary(t)

	r := probe(t, binary, "state", "main", "accessible", "--workdir", empty)
	if r.Value != false {
		t.Fatalf("empty workdir: accessible must be false, got %v", r.Value)
	}
}

// ---------------------------------------------------------------------------
// Scenario 5 — usage errors
// ---------------------------------------------------------------------------
//
// Missing required flags must surface as ErrUsage (exit 1), not silently
// produce misleading zero values. This is the SDK contract from v0.1.2.

func TestScenario_MissingAddressFlag(t *testing.T) {
	binary := buildProviderBinary(t)
	workdir := t.TempDir()

	// Run without --address — no fixture needed, the usage check fires first.
	cmd := exec.Command(binary, "probe", "x", "exists_in_state",
		"--type", "resource", "--workdir", workdir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected non-zero exit, got err=%v stderr=%s", err, stderr.String())
	}
	if code := ee.ExitCode(); code != 1 {
		t.Fatalf("want exit 1 (usage), got %d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--address") {
		t.Fatalf("usage error should mention --address, got: %s", stderr.String())
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			return err
		}
		if err := os.WriteFile(d, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Reserve the unused-import backstop common in go integration suites.
var _ = fmt.Sprintf
