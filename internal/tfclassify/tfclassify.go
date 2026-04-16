// Package tfclassify maps terraform stderr phrasing to the provider SDK's
// sentinel errors. This is the one place in the provider that encodes
// terraform-specific vocabulary — everything else consumes the SDK.
package tfclassify

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mgt-tool/mgtt/sdk/provider"
	"github.com/mgt-tool/mgtt/sdk/provider/shell"
)

// Classify is a shell.ClassifyFn for the terraform CLI.
func Classify(stderr string, runErr error) error {
	if runErr == nil {
		return nil
	}
	if errors.Is(runErr, exec.ErrNotFound) {
		return shell.EnvOnlyClassify(stderr, runErr)
	}
	first := firstLine(stderr)
	lower := strings.ToLower(stderr)
	switch {
	// Resource / module / workspace not present.
	case strings.Contains(lower, "no state"),
		strings.Contains(lower, "resource not found in state"),
		strings.Contains(lower, "workspace") && strings.Contains(lower, "does not exist"),
		strings.Contains(lower, "no configuration files"):
		return fmt.Errorf("%w: %s", provider.ErrNotFound, first)

	// Auth / permission / signed-out cases.
	case strings.Contains(lower, "accessdenied"),
		strings.Contains(lower, "access denied"),
		strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "no valid credential"),
		strings.Contains(lower, "novalidcredential"),
		strings.Contains(lower, "nocredentialproviders"),
		strings.Contains(lower, "error loading credentials"),
		strings.Contains(lower, "unable to locate credentials"),
		strings.Contains(lower, "unable to find credentials"):
		return fmt.Errorf("%w: %s", provider.ErrForbidden, first)

	// State lock is a transient condition — another operation holds it.
	case strings.Contains(lower, "state lock"),
		strings.Contains(lower, "error acquiring the state lock"):
		return fmt.Errorf("%w: %s", provider.ErrTransient, first)

	// Network / backend outage.
	case strings.Contains(lower, "i/o timeout"),
		strings.Contains(lower, "connection refused"),
		strings.Contains(lower, "no such host"),
		strings.Contains(lower, "tls handshake timeout"),
		strings.Contains(lower, "context deadline exceeded"),
		strings.Contains(lower, "temporary failure in name resolution"):
		return fmt.Errorf("%w: %s", provider.ErrTransient, first)
	}
	return fmt.Errorf("%w: %s", provider.ErrEnv, first)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
