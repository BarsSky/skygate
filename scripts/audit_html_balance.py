#!/usr/bin/env python3
"""scripts/audit_html_balance.py — B436 (read-only): is a page's markup actually balanced?

WHY THIS EXISTS
    html/template PARSES a template; it does not VALIDATE the HTML that template emits. A page
    with one extra `</div>`, a duplicated opening `<div class="card">`, or a `<form>` that spans
    `<td>` cells therefore renders, answers 200 and passes every render test — while laying out
    wrong, because everything after the imbalance is nested inside the wrong box. All three were
    measured on the live panel (B436): /admin/telegram opened the same card div twice,
    /admin/derp/relays closed a card in the middle of its form, and /admin/derp/relays opened one
    `<form>` across six table cells, so its Save button sits outside any form in a strict parser.
    The operator described the class as «сбивается компоновка элементов, часть из них наплывают
    друг на друга».

WHAT IT CHECKS
    For every template under --root (default internal/handlers/templates), and for every
    {{define}} block inside a file separately (a file may hold several templates), the emitted
    static markup must be BALANCED: every element of the tracked set is closed by its own end tag,
    in the right order, before the block ends.

    Deliberately invisible: template actions ({{...}}) and comments ({{/* ... */}}), void elements
    (input, br, img, …), XML-style self-closing tags (<x/>), and anything inside a quoted
    attribute value — that last one matters: /my/telegram sets `onclick="this.innerHTML='<i …>'"`,
    and a regex scanner counts that `<i>` as a real element and then reports the closing
    `</button>` as stray. The tokenizer is Python's html.parser precisely because it understands
    quoting; a checker that reports a bug that is not there is worse than no checker.

    One statically "legal" pattern is still reported: opening a tag in one {{if}} branch and
    closing it in another. The static scan cannot follow branches, and the pattern is a markup
    smell anyway — duplicate the whole element inside each branch instead.

USAGE
    python3 scripts/audit_html_balance.py [--root DIR] [--quiet]
    exit 0 — every block is balanced
    exit 1 — at least one block is not (each problem printed as file:line: what)
    exit 2 — the tree is wrong (not a verdict about the templates)
"""
from __future__ import annotations

import argparse
import re
import sys
from html.parser import HTMLParser
from pathlib import Path

TRACKED = {
    "div", "table", "tbody", "thead", "tfoot", "tr", "td", "th", "caption",
    "details", "summary", "aside", "main", "footer", "header", "nav", "section",
    "form", "label", "p", "ul", "ol", "li", "h1", "h2", "h3", "h4", "h5", "h6",
    "pre", "blockquote", "a", "button", "select", "option", "textarea", "span",
    "strong", "em", "b", "i", "dl", "dt", "dd", "fieldset", "legend", "figure",
}
VOID = {
    "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta",
    "param", "source", "track", "wbr",
}

COMMENT_RE = re.compile(r"\{\{/\*.*?\*/\}\}", re.S)
ACTION_RE = re.compile(r"\{\{.*?\}\}", re.S)
DEFINE_RE = re.compile(r"\{\{define\b")


def blank(match: "re.Match[str]") -> str:
    """Replace a span with whitespace of the same shape, so line numbers survive."""
    return "".join("\n" if ch == "\n" else " " for ch in match.group(0))


def strip_template(text: str) -> str:
    text = COMMENT_RE.sub(blank, text)
    return ACTION_RE.sub(blank, text)


class BalanceScan(HTMLParser):
    def __init__(self, path: Path) -> None:
        super().__init__(convert_charrefs=True)
        self.path = str(path)
        self.stack: "list[tuple[str, int]]" = []
        self.problems: "list[str]" = []

    # -- helpers -----------------------------------------------------------------
    def _line(self) -> int:
        return self.getpos()[0]

    def _close(self, name: str, line: int) -> None:
        if self.stack and self.stack[-1][0] == name:
            self.stack.pop()
            return
        if any(entry[0] == name for entry in self.stack):
            while self.stack and self.stack[-1][0] != name:
                open_name, open_line = self.stack.pop()
                self.problems.append(
                    f"{self.path}:{open_line}: <{open_name}> is never closed "
                    f"(</{name}> at line {line} closes a different element)"
                )
            if self.stack:
                self.stack.pop()
            return
        self.problems.append(f"{self.path}:{line}: stray </{name}> — no matching open tag")

    # -- parser callbacks --------------------------------------------------------
    def handle_starttag(self, tag, attrs):
        if tag in VOID or tag not in TRACKED:
            return
        self.stack.append((tag, self._line()))

    def handle_startendtag(self, tag, attrs):
        return  # <x/> closes itself

    def handle_endtag(self, tag):
        if tag in VOID or tag not in TRACKED:
            return
        self._close(tag, self._line())

    def finish(self) -> "list[str]":
        for name, line in self.stack:
            self.problems.append(f"{self.path}:{line}: <{name}> is never closed before the block ends")
        return self.problems


def check_block(text: str, path: Path) -> "list[str]":
    scan = BalanceScan(path)
    scan.feed(text)
    scan.close()
    return scan.finish()


def check_file(path: Path) -> "list[str]":
    try:
        raw = path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        raise SystemExit(f"audit_html_balance: cannot read {path}: {exc}")
    starts = [m.start() for m in DEFINE_RE.finditer(raw)]
    if not starts:
        return check_block(strip_template(raw), path)
    problems: "list[str]" = []
    bounds = starts + [len(raw)]
    for i in range(len(starts)):
        problems.extend(check_block(strip_template(raw[bounds[i]:bounds[i + 1]]), path))
    return problems


def main(argv: "list[str]") -> int:
    ap = argparse.ArgumentParser(add_help=True)
    ap.add_argument("--root", default="internal/handlers/templates")
    ap.add_argument("--quiet", action="store_true", help="print only the summary")
    args = ap.parse_args(argv)

    root = Path(args.root)
    if not root.is_dir():
        print(f"audit_html_balance: no such directory: {root}", file=sys.stderr)
        return 2
    files = sorted(root.rglob("*.html"))
    if not files:
        print(f"audit_html_balance: no *.html under {root}", file=sys.stderr)
        return 2

    all_problems: "list[str]" = []
    for path in files:
        all_problems.extend(check_file(path))

    if not args.quiet:
        for problem in all_problems:
            print(problem)
    if all_problems:
        print(f"html-balance: {len(all_problems)} problem(s) in {len(files)} template(s) — IMBALANCED")
        return 1
    print(f"html-balance: {len(files)} template(s) balanced")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
