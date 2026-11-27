# complexity_scan.awk — B525 block 0: per-function size and branch count for Go sources.
#
# WHY AWK AND NOT gocyclo/gocognit: those are not installed anywhere in this project's
# toolchain, and a ratchet that needs `go install` on every host (and in CI) is a ratchet
# that silently stops running — the class this project has paid for repeatedly. This scan
# is deterministic, dependency-free and good enough for its ONE job: notice when a function
# grows, and fail the gate when it does. It is deliberately approximate (it counts braces
# and branch keywords after stripping comments, strings and runes); the numbers are a
# BUDGET, not an academic complexity score.
#
# The first version of this file had a real bug worth keeping in the comment: it looked for
# backticks BEFORE comments, so a single `// … in `backticks` …` comment flipped the raw
# string state forever and every file after it was swallowed — the scan reported 14
# functions instead of ~4 500 and the report looked *clean*. Hence the explicit state
# machine below (line comment, block comment, "string", 'rune', `raw`) and the per-file
# reset, plus the detector-proof row in check_b525: a scanner that finds nothing must FAIL.
#
# Output: "lines<TAB>cc<TAB>file<TAB>func" for every top-level function.
#   lines — physical lines from the func line to its closing brace
#   cc    — 1 + if/for/case/select/&&/|| occurrences inside the body (cyclomatic-ish)

function code_of(line,   out, i, c, n) {
  out = ""
  n = length(line)
  i = 1
  while (i <= n) {
    c = substr(line, i, 1)
    if (inblock) {
      if (c == "*" && substr(line, i + 1, 1) == "/") { inblock = 0; i += 2; continue }
      i++
      continue
    }
    if (inraw) { if (c == "`") { inraw = 0 }; i++; continue }
    if (instr) {
      if (c == "\\") { i += 2; continue }
      if (c == "\"") { instr = 0 }
      i++
      continue
    }
    if (inrune) {
      if (c == "\\") { i += 2; continue }
      if (c == "'") { inrune = 0 }
      i++
      continue
    }
    if (c == "/" && substr(line, i + 1, 1) == "/") { break }
    if (c == "/" && substr(line, i + 1, 1) == "*") { inblock = 1; i += 2; continue }
    if (c == "`") { inraw = 1; i++; continue }
    if (c == "\"") { instr = 1; i++; continue }
    if (c == "'") { inrune = 1; i++; continue }
    out = out c
    i++
  }
  return out
}

function count_branches(s,   n, t) {
  n = 0
  t = s
  while (match(t, /(^|[^A-Za-z0-9_])(if|for|case|select)([^A-Za-z0-9_]|$)/)) {
    n++
    t = substr(t, RSTART + RLENGTH)
  }
  t = s
  while (match(t, /&&|\|\|/)) {
    n++
    t = substr(t, RSTART + RLENGTH)
  }
  return n
}

function emit() {
  printf "%d\t%d\t%s\t%s\n", FNR - start + 1, cc, FILENAME, name
  infunc = 0
}

FNR == 1 { inraw = 0; inblock = 0; instr = 0; inrune = 0; infunc = 0 }

{
  line = code_of($0)
  if (!infunc) {
    if (line ~ /^func[ \t]/) {
      name = line
      sub(/^func[ \t]+/, "", name)
      if (substr(name, 1, 1) == "(") { sub(/^\([^)]*\)[ \t]*/, "", name) }
      sub(/[(<[].*$/, "", name)
      sub(/[ \t].*$/, "", name)
      start = FNR
      cc = 1 + count_branches(line)
      l = line
      o = gsub(/\{/, "{", l); c = gsub(/\}/, "}", l)
      depth = o - c
      seen = (o > 0)
      infunc = 1
      if (seen && depth <= 0) { emit() }
      next
    }
    next
  }
  cc += count_branches(line)
  l = line
  o = gsub(/\{/, "{", l); c = gsub(/\}/, "}", l)
  if (o > 0) { seen = 1 }
  depth += o - c
  if (seen && depth <= 0 && (o + c) > 0) { emit() }
}

END {
  if (infunc) { printf "%d\t%d\t%s\t%s\n", FNR - start + 1, cc, FILENAME, name }
}
