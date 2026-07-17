#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
status=0

"${script_dir}/backup-worker-recovery.sh" || status=$?
"${script_dir}/restore-check-recovery.sh" || status=$?

exit "${status}"
