#!/bin/bash
# Records the DAP messages VS Code exchanges with Delve or debugpy in a
# scripted session (see extension.js for the scenario format), for comparison
# with dap-debugger. Opens a VS Code window with a throwaway profile.
#
# usage: tools/vscode-probe/run.sh SCENARIO.json OUTPUT.json
set -euo pipefail
script=$(cd "$(dirname "$1")" && pwd)/$(basename "$1")
out=$(cd "$(dirname "$2")" && pwd)/$(basename "$2")
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
# VS Code's IPC socket path must stay short, so the profile lives in $HOME.
profile=$HOME/.vsc-probe-$$
rm -f "$out"
cleanup() {
  pkill -f "user-data-dir $profile" || true
  # VS Code writes to its profile while quitting, so wait before removing it.
  for _ in $(seq 1 50); do
    pgrep -f "user-data-dir $profile" >/dev/null || break
    sleep 0.2
  done
  rm -rf "$profile"
}
trap cleanup EXIT

# VS Code merges the login shell's environment over ours, so the probe gets
# the .venv and dlv under names the shell does not set.
export PROBE_VENV=$repo/.venv
if ! "$PROBE_VENV/bin/python" -c "import debugpy" 2>/dev/null; then
  echo "$PROBE_VENV/bin/python cannot import debugpy; run 'uv sync' to create it" >&2
  exit 1
fi
PROBE_DLV=${PROBE_DLV:-$(command -v dlv || true)}
if [ -z "$PROBE_DLV" ] && grep -Eq '"type": *"go"' "$script"; then
  echo "dlv not found; put it on PATH or set PROBE_DLV" >&2
  exit 1
fi
export PROBE_DLV

code=${VSCODE:-"/Applications/Visual Studio Code.app/Contents/MacOS/Code"}
PROBE_OUT=$out PROBE_SCRIPT=$script \
  "$code" --user-data-dir "$profile" --extensions-dir "$profile/ext" \
  --extensionDevelopmentPath="$here" --new-window --disable-workspace-trust \
  --skip-welcome --skip-release-notes "$repo/examples" >/dev/null 2>&1 &

for _ in $(seq 1 150); do
  [ -s "$out" ] && exit 0
  sleep 1
done
echo "VS Code did not finish the session" >&2
exit 1
