#!/usr/bin/env bash
# Runs deadvalues for the GitHub Action and builds the Markdown report used for
# the job summary and the pull request comment. The inputs come from the
# environment set in action.yml. The script itself always exits 0; the exit
# status of deadvalues goes to the "status" output and a later step fails the
# job, so the comment is still posted when there are findings.
set -uo pipefail

work="$RUNNER_TEMP/deadvalues-run"
report="$RUNNER_TEMP/deadvalues-report.md"
rm -rf "$work"
mkdir -p "$work"

docs="https://github.com/guarnz/deadvalues"
zero=0000000000000000000000000000000000000000
fence='```'
status=0
n=0
base_line=""
note=""
blocker=""

# jq filters over the JSON reports: whether there is something worth showing,
# and the counts quoted in the alert at the top.
diff_finds='[.] | flatten | any(.error != null or any(.changes[]?; .ignored != true))'
check_finds='[.] | flatten | any(.error != null or any(.keys[]?; .ignored != true and (.usedBy // "") == "" and ((.sharedWith // []) | length) == 0 and (.status | test("^(DEAD|CONDITIONAL|REDUNDANT)"))))'
diff_counts='[.] | flatten | "\(map(.counts.BREAKING // 0) | add // 0) breaking, \(map(.counts.PINNED // 0) | add // 0) pinned, \(map(.counts.CHANGED // 0) | add // 0) changed, \(map(.counts.FIXED // 0) | add // 0) fixed"'
check_counts='[.] | flatten | "\(map(.counts.DEAD // 0) | add // 0) dead, \(map(.counts.CONDITIONAL // 0) | add // 0) conditional, \(map(.counts.REDUNDANT // 0) | add // 0) redundant"'
errors='[.] | flatten | map(select(.error != null)) | length'

# dv runs one deadvalues command in its own log group and keeps everything the
# report needs. Commands with a Markdown report also write JSON, which decides
# whether their section is shown.
dv() {
  n=$((n + 1))
  local id="$work/$n" rc
  local files=()
  echo "deadvalues $*" > "$id.cmd"
  echo "${1:-}" > "$id.name"
  case "${1:-}" in
    check|scan|diff) files=(--output-file "markdown=$id.md" --output-file "json=$id.json") ;;
    explain) files=(--output-file "markdown=$id.md") ;;
  esac
  echo "::group::deadvalues $*"
  deadvalues "$@" ${files[@]+"${files[@]}"} 2> "$id.err" | tee "$id.out"
  rc=${PIPESTATUS[0]}
  cat "$id.err" >&2
  echo "::endgroup::"
  echo "$rc" > "$id.rc"
  if [ "$rc" -gt "$status" ]; then status=$rc; fi
}

# state prints error, findings, clean or output for the command with id $1.
state() {
  local id="$work/$1" rc filter
  rc="$(cat "$id.rc")"
  if [ "$rc" -ge 2 ]; then echo error; return; fi
  if [ "$rc" -eq 1 ]; then echo findings; return; fi
  case "$(cat "$id.name")" in
    diff) filter="$diff_finds" ;;
    check|scan) filter="$check_finds" ;;
    *) echo output; return ;;
  esac
  if [ ! -s "$id.json" ] || ! command -v jq >/dev/null; then echo findings; return; fi
  if jq -e "$filter" "$id.json" >/dev/null 2>&1; then echo findings; else echo clean; fi
}

# summary prints the counts of the command with id $1 for the alert.
summary() {
  local id="$work/$1" name filter counts failed
  name="$(cat "$id.name")"
  case "$name" in
    diff) filter="$diff_counts" ;;
    check|scan) filter="$check_counts" ;;
    *) return ;;
  esac
  if [ ! -s "$id.json" ] || ! command -v jq >/dev/null; then
    echo "> - \`$name\`: could not run, see below."
    return
  fi
  counts="$(jq -r "$filter" "$id.json" 2>/dev/null)"
  failed="$(jq -r "$errors" "$id.json" 2>/dev/null)"
  if [ "${failed:-0}" -gt 0 ]; then counts="$counts, $failed not analyzed"; fi
  echo "> - \`$name\`: $counts"
}

section() {
  local id="$work/$1"
  if [ -s "$id.md" ]; then
    cat "$id.md"
  elif [ -s "$id.out" ]; then
    printf '%stext\n%s\n%s\n\n' "$fence" "$(cat "$id.out")" "$fence"
  fi
  if [ "$(cat "$id.rc")" -ge 2 ] && [ -s "$id.err" ]; then
    printf '%stext\n%s\n%s\n\n' "$fence" "$(cat "$id.err")" "$fence"
  fi
}

case "$MODE" in
  all|diff|scan) ;;
  *)
    echo "::error title=deadvalues::mode must be all, diff or scan, got \"$MODE\""
    exit 2
    ;;
esac

base=""
if [ -z "${ARGS:-}" ]; then
  if [ -n "${BASE_INPUT:-}" ]; then
    base="$BASE_INPUT"
    base_line="Base: \`$base\` (the \`base\` input)"
  else
    from=""
    if [ -n "${PR_BASE:-}" ]; then
      base="$PR_BASE"
      from="the pull request base"
    elif [ -n "${PUSH_BEFORE:-}" ] && [ "$PUSH_BEFORE" != "$zero" ]; then
      base="$PUSH_BEFORE"
      from="the commit before the push"
    fi
    if [ -n "$base" ]; then
      if ! git cat-file -e "$base^{commit}" 2>/dev/null; then
        git fetch --no-tags --depth=1 origin "$base" >/dev/null 2>&1 || true
      fi
      if git cat-file -e "$base^{commit}" 2>/dev/null; then
        base_line="Base: \`${base:0:7}\` ($from)"
      else
        note="The base, $from (\`${base:0:7}\`), is not in the repository, e.g. after a force push."
        base=""
      fi
    fi
  fi
fi

diff_fail=()
if [ -n "${FAIL_ON_DIFF:-}" ]; then diff_fail=(--fail-on "$FAIL_ON_DIFF"); fi
scan_fail=()
if [ -n "${FAIL_ON_SCAN:-}" ]; then scan_fail=(--fail-on "$FAIL_ON_SCAN"); fi

if [ -n "${ARGS:-}" ]; then
  read -ra argv <<< "$ARGS"
  dv "${argv[@]}"
elif [ "$MODE" = "diff" ] && [ -z "$base" ]; then
  echo "::error title=deadvalues::mode diff needs a base: run on pull_request or push, or set the base input"
  blocker="Nothing ran: mode \`diff\` needs a base. Run on \`pull_request\` or \`push\`, or set the \`base\` input."
  status=2
else
  if [ -n "$base" ] && [ "$MODE" != "scan" ]; then
    dv diff "$APPS" --base "$base" ${diff_fail[@]+"${diff_fail[@]}"}
  fi
  if [ "$MODE" != "diff" ]; then
    if [ -n "$base" ]; then
      dv scan "$APPS" --changed-since "$base" --details ${scan_fail[@]+"${scan_fail[@]}"}
    else
      if [ -z "$note" ]; then note="No base to compare with (no pull request, or the first push of a branch)."; fi
      note="$note Every app was scanned."
      dv scan "$APPS" --details ${scan_fail[@]+"${scan_fail[@]}"}
    fi
  fi
fi

states=()
shown=()
found=0
i=1
while [ "$i" -le "$n" ]; do
  s="$(state "$i")"
  states+=("$s")
  if [ "$s" != "clean" ]; then shown+=("$i"); fi
  if [ "$s" = "findings" ] || [ "$s" = "error" ]; then found=1; fi
  i=$((i + 1))
done

{
  echo "<!-- deadvalues -->"
  echo "## deadvalues"
  echo
  if [ -n "$blocker" ]; then
    echo "> [!CAUTION]"
    echo "> $blocker"
    echo
  elif [ "$found" -eq 1 ]; then
    if [ "$status" -ge 2 ]; then
      echo "> [!CAUTION]"
      echo "> deadvalues could not analyze everything, see the errors below."
    elif [ "$status" -eq 1 ]; then
      echo "> [!CAUTION]"
      echo "> Findings at the fail-on level."
    else
      echo "> [!WARNING]"
      echo "> Findings below the fail-on level, nothing fails."
    fi
    echo ">"
    for i in ${shown[@]+"${shown[@]}"}; do summary "$i"; done
    echo
  fi
  if [ -n "$note" ]; then
    echo "> [!NOTE]"
    echo "> $note"
    echo
  fi
  if [ -n "$base_line" ]; then
    echo "$base_line · [Docs]($docs)"
  else
    echo "[Docs]($docs)"
  fi
  echo
  i=1
  while [ "$i" -le "$n" ]; do
    rc="$(cat "$work/$i.rc")"
    case "${states[$((i - 1))]}" in
      error) mark=":x: " ;;
      findings) if [ "$rc" -eq 1 ]; then mark=":x: "; else mark=":warning: "; fi ;;
      clean) mark=":white_check_mark: " ;;
      *) mark="" ;;
    esac
    echo "- $mark\`$(cat "$work/$i.cmd")\`"
    i=$((i + 1))
  done
  echo
  if [ "$n" -gt 0 ] && [ "${#shown[@]}" -eq 0 ]; then
    echo "No findings."
    echo
  fi
  for i in ${shown[@]+"${shown[@]}"}; do
    echo "---"
    echo
    echo "**\`$(cat "$work/$i.cmd")\`**"
    echo
    section "$i"
  done
} > "$report"

case "$status" in
  0) echo "deadvalues: nothing at or above the fail-on levels" ;;
  1) echo "::error title=deadvalues::findings at or above the fail-on levels, see the job summary" ;;
  *) echo "::error title=deadvalues::deadvalues could not analyze every target, see the job summary" ;;
esac

cat "$report" >> "$GITHUB_STEP_SUMMARY"
{
  echo "report=$report"
  echo "status=$status"
  echo "base=$base"
} >> "$GITHUB_OUTPUT"
