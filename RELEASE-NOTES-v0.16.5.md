# v0.16.5 — split long bot replies into multiple bubbles

2026-07-16

The operator reported that on a phone, long bot replies
(/help, /audit, /my_rules) are hard to scan because
Telegram's default font is small and the entire reply
sits in one bubble. Telegram doesn't support font-size
changes in its HTML subset, so the cleanest fix is to
**break long replies into multiple shorter bubbles**
— each section gets its own screen real estate, the
bubble boundary acts as a visual break, and the eye
doesn't have to track one giant scroller.

## What changed

### 1. Sentinel marker + split in send path

`internal/telegram/commands.go` adds
`splitMessageMarker` — a non-printing sentinel string
(`\n\n\x00SPLIT\x00\n\n`) that a reply function uses
to mark "send a new message here".

`internal/telegram/notify.go` `RealNotifier.reply`
detects the marker and splits the body before issuing
sendMessage calls. The first part keeps the inline
keyboard (if any) and `parse_mode=HTML`; subsequent
parts are plain HTML bubbles (no keyboard so it
doesn't repeat under each section).

The `splitReplyParts` helper trims whitespace around
the split point and drops empty parts.

### 2. /help — 3 bubbles (was 1)

`helpReply` now sends each of the three sections as
its own message:

| Layout | Bubbles | Section breakdown |
|--------|---------|-------------------|
| Admin (`!IsIdentified \|\| IsAdmin`) | 3 | Auth → User-scope → Admin |
| User (`IsIdentified && !IsAdmin`) | 2 | Auth → User-scope |
| Locked (`!IsIdentified && StrictMode`) | 1 | (locked note + Auth) |

The title `<b>Bot codex ...</b>` and the subtitle
`<i>Commands below ...</i>` go in the FIRST bubble
so the operator knows which command produced the
burst.

### 3. /audit — split if > 10 entries

`auditReply` splits the log into 2 bubbles at 10
entries (the LIMIT 20 in the SQL returns 20 max, so
under 10 the reply is short enough to not need
splitting; over 10 the split gives 2 focused
bubbles of 10 each). The split point is at the
first blank-line boundary AFTER the (entries/2)th
row, so the second bubble starts at a clean row
(not in the middle of a detail line).

The first bubble ends with `<i>(N more — see next
message)</i>` (catalog: `bot.audit.split_more`)
so the operator knows the second bubble is coming.

### 4. /my_rules — split if > 12 rules

`myRulesReply` splits the user's own exit-rules
into 2 bubbles at 12 rules (most users have 1-10
rules; 12 is the threshold where the table starts
feeling crowded on a phone). Same hint pattern as
/audit (`bot.my_rules.split_more`).

### 5. Why not /myexitnodes, /my_nodes, /my_status?

These are short by construction:
- /myexitnodes: 1-5 exit-nodes in the typical
  deployment
- /my_nodes: usually 1-3 devices per user
- /my_status: 3 Field() lines, very compact

Splitting them would be visual noise without a
readability win. The threshold is left at the
default 0; if a future deployment routinely has
50+ nodes per user, add a split there too.

## Why we can't make text BIGGER

Telegram's HTML subset (`<b>`, `<i>`, `<u>`, `<s>`,
`<code>`, `<pre>`, `<a>`, `<tg-spoiler>`) has no
font-size tag. The only "big text" mechanism is
per-message Big Emoji mode (a single emoji at the
start of a line is rendered larger), but that's
just the emoji — the rest of the line is the
default size. So splitting into multiple bubbles
is the next-best mitigation:

- Each bubble gets its own screen on mobile
- The bubble boundary acts as a visual break
- The eye can dismiss one section at a time
- The first bubble's bold title is the strongest
  cue the user gets for "this is a new section"

If Telegram adds a font-size tag to its HTML
subset in the future, the split can be reverted
in favor of bigger text in a single bubble.

## Tests

- 3 new tests:
  - `TestSplitReplyParts` (6 sub-cases for the
    split helper itself)
  - `TestHelpReplyUserSplitsIntoTwoBubbles` (user
    layout: 2 bubbles, no admin section)
  - `TestAuditReplySplitLongLog` / 
    `TestAuditReplyNoSplitShortLog` (long / short
    log boundary)
  - `TestMyRulesReplySplitLongList` (15 rules:
    split; 1 rule: no split)
- All existing v0.16.1–v0.16.4 tests still pass
- 12/12 packages green

## Operator impact

After `docker compose up -d --force-recreate --no-deps
skygate`:

- `/help` arrives as 2-3 bubbles instead of 1,
  each focused on one section (Auth / User-scope /
  Admin)
- `/audit` (with > 10 entries) arrives as 2
  bubbles, first ending with a "more in next
  message" hint
- `/my_rules` (with > 12 rules) arrives as 2
  bubbles, same hint pattern
- All other replies are unchanged (no split)
- No config changes, no schema migration, no
  ACL changes
- Existing admin handlers don't care about the
  split (they iterate over `pendingReplyForCurrentMessage`
  on the first bubble only, which is the v0.15.3
  behavior)
