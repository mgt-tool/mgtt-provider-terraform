// Command mgtt-provider-terraform is a Terraform provider runner binary for
// mgtt. All plumbing (argv parsing, JSON output, exit codes, timeouts,
// status:not_found translation) lives in the mgtt SDK at
// github.com/mgt-tool/mgtt/sdk/provider — this file only wires probes.
package main

import (
	"github.com/mgt-tool/mgtt-provider-terraform/internal/probes"
	"github.com/mgt-tool/mgtt/sdk/provider"
)

func main() {
	r := provider.NewRegistry()
	probes.Register(r)
	provider.Main(r)
}
