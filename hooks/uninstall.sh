#!/bin/bash
set -euo pipefail

# Runs from inside the installed provider dir ($MGTT_PROVIDER_DIR) before
# mgtt removes the directory. Even on failure, mgtt still removes the dir
# — uninstall must always succeed. Keep cleanup simple and idempotent.
#
# NOTE: this hook does NOT touch any user-configured terraform workdir or
# state. It only removes artifacts this provider built locally.

cd "$(dirname "$0")/.."

# Built binary + any go-build leftovers local to this provider.
rm -rf bin/

echo "✓ terraform provider cleaned up"
