#!/bin/bash
# Mock OMP — simulates skill behavior by applying frontmatter transitions.
# Usage: omp --model <model> [--auto-approve] -p "<skill_route> <taskfile>"
# The task file path is the last word of the -p value.
set -euo pipefail

model=""
taskfile=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --model)
      model="$2"
      shift 2
      ;;
    --auto-approve)
      # Accept but ignore
      shift
      ;;
    --approval-mode)
      # Accept but ignore; next token is the mode value
      shift 2
      ;;
    -p)
      # Format: "/obsidian-task-runner-<skill> /path/to/taskfile"
      # The last space-delimited word is the task file path
      taskfile="${2##* }"
      shift 2
      ;;
    *)
      # Fallback: positional arg as task file path
      if [[ -z "$taskfile" && -f "$1" ]]; then
        taskfile="$1"
      fi
      shift
      ;;
  esac
done

echo "[fake-omp] model=$model taskfile=$taskfile" >&2

if [[ ! -f "$taskfile" ]]; then
  echo "[fake-omp] ERROR: task file not found: $taskfile" >&2
  exit 1
fi

# Read current status from YAML frontmatter (between first two --- delimiters)
raw=$(awk '/^---$/{if(!seen){seen=1;next}} /^---$/{exit} seen' "$taskfile")
current_status=$(echo "$raw" | grep '^status:' | sed 's/status:[[:space:]]*//' | tr -d '"' | tr -d '[:space:]')
echo "[fake-omp] status=$current_status" >&2

case "$current_status" in
  refining)
    # Simulate refining skill: mark maturity fully_mature, bump plan_version,
    # transition to planning (where the daemon picks it up next cycle).
    sed -i 's/^status:.*$/status: planning/' "$taskfile"
    # Set or replace maturity
    if grep -q '^maturity:' "$taskfile"; then
      sed -i 's/^maturity:.*$/maturity: fully_mature/' "$taskfile"
    else
      sed -i '/^---$/a\maturity: fully_mature' "$taskfile"
    fi
    # Bump plan_version
    pv=$(echo "$raw" | grep '^plan_version:' | sed 's/plan_version:[[:space:]]*//' | tr -d '[:space:]')
    pv=$((pv + 1))
    sed -i "s/^plan_version:.*$/plan_version: $pv/" "$taskfile"
    # Bump refine_version too for audit trail
    rv=$(echo "$raw" | grep '^refine_version:' | sed 's/refine_version:[[:space:]]*//' | tr -d '[:space:]')
    rv=$((rv + 1))
    sed -i "s/^refine_version:.*$/refine_version: $rv/" "$taskfile"
    echo "[fake-omp] refining→planning (maturity=fully_mature, plan_version=$pv, refine_version=$rv)" >&2
    ;;
  planning)
    # Simulate round1 skill: bump plan_version, transition to plan-review
    pv=$(echo "$raw" | grep '^plan_version:' | sed 's/plan_version:[[:space:]]*//' | tr -d '[:space:]')
    pv=$((pv + 1))
    sed -i "s/^plan_version:.*$/plan_version: $pv/" "$taskfile"
    sed -i 's/^status:.*$/status: plan-review/' "$taskfile"
    echo "[fake-omp] planning→plan-review (plan_version=$pv)" >&2
    ;;
  implementing)
    # Simulate round2 skill: transition to review for human gate
    sed -i 's/^status:.*$/status: review/' "$taskfile"
    echo "[fake-omp] implementing→review" >&2
    ;;
  *)
    echo "[fake-omp] no transition defined for status=$current_status" >&2
    ;;
esac

exit 0
