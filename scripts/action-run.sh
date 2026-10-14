#!/usr/bin/env bash
# Runs deadvalues for the GitHub Action and builds the Markdown report used for
# the job summary and the pull request comment. The inputs come from the
# environment set in action.yml. The script itself always exits 0; the exit
# status of deadvalues goes to the "status" output and a later step fails the
# job, so the comment is still posted when there are findings.
# shellcheck disable=SC2016
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
note=""
blocker=""

# dv runs one deadvalues command in its own log group and keeps everything the
# report needs: check, scan and diff also write JSON, explain only Markdown.
dv() {
  n=$((n + 1))
  local id="$work/$n" rc
  local files=()
  echo "deadvalues $*" > "$id.cmd"
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

rows_jq=$(cat <<'EOF'
def rank: {"BREAKING":0,"ERROR":1,"PINNED":2,"DEAD":3,"CHANGED":4,"FIXED":5,"REDUNDANT":6,"CONDITIONAL":7}[.] // 8;
def rel: if $ws != "" and startswith($ws + "/") then .[($ws | length) + 1:] else . end;
[.[] | if type == "array" then .[] else . end] as $reports
| [$reports[] | . as $r
    | if (.error // "") != "" then {app: .app, file: (.appFile // ""), head: (.chart // ""), kind: "ERROR", key: "", detail: .error, src: null}
      elif has("changes") then .changes[]? | select(.ignored != true)
        | {app: $r.app, file: ($r.appFile // ""), head: "chart \($r.from) → \($r.to)", kind: .kind, key: .key, detail: .detail, src: (.origins // [])[0], diff: true}
      elif has("keys") then .keys[]?
        | select(.ignored != true and (.usedBy // "") == "" and ((.sharedWith // []) | length) == 0 and (.status | test("^(DEAD|CONDITIONAL|REDUNDANT)")))
        | {app: $r.app, file: ($r.appFile // ""), head: ($r.chart // ""), kind: (.status | split(" ")[0]), key: .key, detail: .detail, src: (.origins // [])[0]}
      else empty end
    | .rank = (.kind | rank)]
| (map(select(.diff) | "\(.app) \(.key)")) as $seen
| map(select(.diff or (("\(.app) \(.key)") as $k | $seen | index($k) | not))) as $rows
EOF
)

body_jq=$(cat <<'EOF'
def mark: if . <= 1 then "-" elif . <= 3 then "!" elif . == 5 then "+" else "±" end;
def light: if . <= 1 then "-" elif . <= 3 then "!" else "+" end;
def heading: .[0:1] + (.[1:] | ascii_downcase);
def cell: gsub("\\|"; "\\|") | gsub("\n"; " ") | gsub("  +"; " ");
def pad($w): . + (if $w > length then " " * ($w - length) else "" end);
def src: if . == null or (.file // "") == "" then ""
  else (.file | rel) as $f | (($f | split("/") | last) + (if .line then ":\(.line)" else "" end)) as $t
  | if $blob != "" then "[\($t)](\($blob)/\($f)\(if .line then "#L\(.line)" else "" end))" else "`\($t)`" end end;
($rows | map(.app | length) | max // 0) as $w
| ($rows | group_by(.app) | sort_by((map(.rank) | min), .[0].app)) as $apps
| ($apps | map(
    "\(map(.rank) | min | light) \(.[0].app | pad($w))  "
    + (group_by(.rank) | map("\(length) \(.[0].kind | ascii_downcase)") | join(" · "))
  ) | join("\n")) as $byapp
| ($rows | group_by(.rank) | map(
    "\(.[0].kind | heading):\n"
    + (map("\(.rank | mark) \(.app | pad($w))  \(if .key != "" then .key else "not analyzed" end)") | join("\n"))
  ) | join("\n\n")) as $groups
| "Findings (\($apps | length) app\(if ($apps | length) == 1 then "" else "s" end)):\n\($byapp)\n\n\($groups)" as $summary
| ($apps | map(
    (map(select(.head | startswith("chart "))) | first // null) as $bump
    | (if $bump != null then $bump.head else .[0].head end) as $head
    | "<details\(if any(.rank <= 1) then " open" else "" end)><summary><b>\(.[0].app)</b>"
      + (if .[0].file != "" then " (\(.[0].file | rel))" else "" end)
      + (if $head != "" then " · \($head)" else "" end) + "</summary>\n\n"
      + "| Finding | Key | Detail | Line |\n|---|---|---|---|\n"
      + (sort_by(.rank) | map("| \(if .rank <= 1 then "**\(.kind)**" else .kind end) | \(if .key != "" then "`\(.key)`" else "" end) | \(.detail | cell) | \(.src | src) |") | join("\n"))
      + "\n</details>"
  ) | join("\n\n")) as $details
| "Summary:\n\n```diff\n\($summary)\n```\n\n\($details)\n"
EOF
)

stats_jq='"[Apps: \($reports | map(select((.renders // 0) > 0 or (.error // "") != "") | .app) | unique | length)], [Renders: \($reports | map(.renders // 0) | add // 0)], [Run: \((($reports | map(.durationMs // 0) | add // 0) / 1000 | ceil))s]"'

# output prints what the command with id $1 wrote and, when it failed, its errors.
output() {
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

# summary prints the report: findings from every JSON report in one place, and
# the output of commands without one (explain, or a command that failed).
summary() {
  local i rows=0 breaking=0 stats="" blob="" run="" footer
  local jsons=() others=() has_jq=1
  command -v jq >/dev/null || has_jq=0
  for ((i = 1; i <= n; i++)); do
    if [ "$has_jq" -eq 1 ] && [ -s "$work/$i.json" ]; then jsons+=("$work/$i.json"); else others+=("$i"); fi
  done
  if [ -n "${GITHUB_SERVER_URL:-}" ] && [ -n "${GITHUB_REPOSITORY:-}" ]; then
    blob="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/blob/${GITHUB_SHA:-HEAD}"
    if [ -n "${GITHUB_RUN_ID:-}" ]; then run="$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID"; fi
  fi
  q() { jq -rs --arg ws "${GITHUB_WORKSPACE:-}" --arg blob "$blob" "$rows_jq | $1" "${jsons[@]}"; }
  if [ "${#jsons[@]}" -gt 0 ]; then
    rows="$(q '$rows | length')"
    breaking="$(q '$rows | map(select(.kind == "BREAKING")) | length')"
    stats="$(q "$stats_jq")"
  fi

  footer="_Stats_: "
  if [ -n "$stats" ]; then footer="$footer$stats"; fi
  if [ -n "$base" ]; then footer="${footer}${stats:+, }[Base: ${base:0:7}]"; fi
  if [ "$footer" = "_Stats_: " ]; then footer=""; else footer="$footer · "; fi
  if [ -n "$run" ]; then footer="${footer}[Run]($run) · "; fi
  footer="${footer}[Docs]($docs)"

  echo "<!-- deadvalues -->"
  echo "## Deadvalues"
  echo
  if [ -n "$blocker" ]; then
    printf '> [!CAUTION]\n> %s\n\n' "$blocker"
  elif [ "$status" -ge 2 ]; then
    printf '> [!CAUTION]\n> deadvalues could not analyze everything, see the errors below. The check fails.\n\n'
  elif [ "$status" -eq 1 ]; then
    if [ "${breaking:-0}" -eq 1 ]; then
      printf '> [!CAUTION]\n> 1 breaking change: a chart upgrade stops reading a key you set. The check fails.\n\n'
    elif [ "${breaking:-0}" -gt 1 ]; then
      printf '> [!CAUTION]\n> %s breaking changes: a chart upgrade stops reading keys you set. The check fails.\n\n' "$breaking"
    else
      printf '> [!CAUTION]\n> Findings at the fail-on level. The check fails.\n\n'
    fi
  fi
  if [ -n "$note" ]; then printf '> [!NOTE]\n> %s\n\n' "$note"; fi
  if [ "$has_jq" -eq 0 ] && [ "$n" -gt 0 ]; then
    printf '> [!NOTE]\n> jq is not installed on the runner, so the findings are shown per command.\n\n'
  fi

  if [ "${rows:-0}" -gt 0 ]; then
    q "$body_jq"
  elif [ "$n" -gt 0 ] && [ "${#others[@]}" -eq 0 ]; then
    if [ -n "$base" ]; then echo "No findings in the changed apps."; else echo "No findings."; fi
    echo
  fi
  for i in ${others[@]+"${others[@]}"}; do
    if [ "$(cat "$work/$i.rc")" -ge 2 ]; then
      printf '<details open><summary>✗ <code>%s</code> failed</summary>\n\n' "$(cat "$work/$i.cmd")"
      output "$i"
      printf '</details>\n\n'
    else
      printf '**`%s`**\n\n' "$(cat "$work/$i.cmd")"
      output "$i"
      echo
    fi
  done

  if [ "$n" -gt 0 ]; then
    printf '<details><summary>Commands</summary>\n\n'
    for ((i = 1; i <= n; i++)); do
      if [ -n "$base" ]; then
        echo "- \`$(sed "s/$base/${base:0:7}/g" "$work/$i.cmd")\`"
      else
        echo "- \`$(cat "$work/$i.cmd")\`"
      fi
    done
    printf '\n</details>\n\n'
  fi
  echo "$footer"
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
      if ! git cat-file -e "$base^{commit}" 2>/dev/null; then
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

summary > "$report"

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
