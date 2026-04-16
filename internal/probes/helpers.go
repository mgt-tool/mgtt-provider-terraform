// Package probes implements the terraform-provider probe surface. All
// plumbing (argv parsing, status translation, exit codes) lives in the SDK;
// this package only constructs terraform argv and parses output.
package probes

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mgt-tool/mgtt-provider-terraform/internal/tfclassify"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

// TF wraps invocations of the terraform CLI scoped to a working directory
// and workspace. Workspace is applied via TF_WORKSPACE env var on the
// child process — NOT via `terraform workspace select`, which would mutate
// the caller's on-disk .terraform/environment and race with concurrent
// human runs.
type TF struct {
	Binary    string
	Workdir   string
	Workspace string

	// Exec is the low-level runner. Tests swap it for a fake.
	Exec func(ctx context.Context, dir, workspace string, args ...string) (stdout, stderr []byte, exitCode int, runErr error)
}

// New returns a TF configured for the given workdir + workspace + binary.
// The returned TF is the package default; tests override
// NewTFConstructor to inject fakes.
var NewTFConstructor = newTF

func newTF(workdir, workspace, binary string) *TF {
	if binary == "" {
		binary = "terraform"
	}
	return &TF{
		Binary:    binary,
		Workdir:   workdir,
		Workspace: workspace,
		Exec: func(ctx context.Context, dir, ws string, args ...string) ([]byte, []byte, int, error) {
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Dir = dir
			cmd.Env = os.Environ()
			if ws != "" && ws != "default" {
				cmd.Env = append(cmd.Env, "TF_WORKSPACE="+ws)
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
				err = nil // exit code is the signal; don't treat as a run error
			}
			return out, []byte(stderr.String()), code, err
		},
	}
}

// Run invokes `terraform <args>`. Non-zero exit that came from a plain
// process completion is returned as nil error with the exit code; callers
// inspect it directly when using -detailed-exitcode. Real run errors are
// classified via tfclassify.
func (t *TF) Run(ctx context.Context, args ...string) (stdout []byte, exitCode int, err error) {
	stdout, stderr, code, runErr := t.Exec(ctx, t.Workdir, t.Workspace, args...)
	if runErr != nil {
		return nil, 0, tfclassify.Classify(string(stderr), runErr)
	}
	if code != 0 && !acceptsExitCode(args) {
		// Non-zero exit but no exec-level error: terraform failed gracefully.
		// Classify via stderr + a synthetic error so tfclassify has a non-nil
		// runErr to trigger its switch.
		if len(stderr) == 0 {
			return stdout, code, nil
		}
		return nil, code, tfclassify.Classify(string(stderr),
			fmt.Errorf("exit status %d", code))
	}
	return stdout, code, nil
}

// acceptsExitCode reports whether args contain the exact -detailed-exitcode
// flag, in which case exit 2 is a semantic signal.
func acceptsExitCode(args []string) bool {
	for _, a := range args {
		if a == "-detailed-exitcode" {
			return true
		}
	}
	return false
}

// StateList runs `terraform state list` and returns one address per line.
func (t *TF) StateList(ctx context.Context) ([]string, error) {
	out, _, err := t.Run(ctx, "state", "list")
	if err != nil {
		return nil, err
	}
	var addrs []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			addrs = append(addrs, line)
		}
	}
	return addrs, nil
}

// StateFileMtimeAge returns seconds since the local state file was modified,
// or 0 when no local state file exists at the default path.
func StateFileMtimeAge(workdir string) int {
	fi, err := os.Stat(workdir + "/terraform.tfstate")
	if err != nil {
		return 0
	}
	return int(time.Since(fi.ModTime()).Seconds())
}

// ResolveWorkdir extracts workdir from request extras; every probe needs it.
func ResolveWorkdir(req provider.Request) (string, error) {
	if w := req.Extra["workdir"]; w != "" {
		return w, nil
	}
	return "", fmt.Errorf("%w: terraform provider requires --workdir <path>", provider.ErrUsage)
}

// ResolveBinary returns the terraform binary path, defaulting to "terraform".
func ResolveBinary(req provider.Request) string {
	if b := req.Extra["tf_bin"]; b != "" {
		return b
	}
	return "terraform"
}

// ResolveWorkspace returns the workspace name; empty string means default.
func ResolveWorkspace(req provider.Request) string {
	return req.Extra["workspace"]
}

// newFromReq constructs a TF helper from request extras. Workspace is
// attached as TF_WORKSPACE env var on every child invocation; no on-disk
// `.terraform/environment` mutation.
func newFromReq(req provider.Request) (*TF, error) {
	workdir, err := ResolveWorkdir(req)
	if err != nil {
		return nil, err
	}
	return NewTFConstructor(workdir, ResolveWorkspace(req), ResolveBinary(req)), nil
}

// Register adds the terraform provider's types to the registry.
func Register(r *provider.Registry) {
	registerState(r)
	registerResource(r)
}

// Unused-symbol suppression handled by actual references above.
var _ = time.Now
