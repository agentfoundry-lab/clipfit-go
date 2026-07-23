---
name: clipfit
description: Modify or create local files through ClipFit MCP using policy-selected reviewable preview/apply transactions or direct edits, with exact match validation, unique anchors, compact write receipts, rollback backups, and a mandatory filesystem root. Use for reliable code edits, batch replacements, identifier swaps, new-file creation, or projects whose AGENTS.md defines risk-based editing rules; prefer MCP tools over the legacy CLI workflow.
---

# ClipFit safe file editor

ClipFit exposes structured MCP tools for reliable local file edits. Existing
files can use a reviewable preview/apply transaction or an optional direct edit;
new files use a create-only operation, and every write is constrained to the
server's configured filesystem root.

The server must be launched with an explicit, non-empty `--root`; it refuses to
start without one. That root is the immutable maximum scope for the server's
lifetime and cannot be widened by an agent tool call. A project root offers
stronger isolation, while a home-directory root permits cross-project edits with
root-relative paths. ClipFit can therefore provide structured writes while the
agent's general shell remains sandboxed, without granting `danger-full-access` or
broad shell write permissions. It does not bypass operating-system permissions;
the ClipFit process still needs normal read/write access to the selected root.

## MCP workflow

Read the closest applicable `AGENTS.md` before editing and follow its mode policy.
Choose one existing-file workflow:

- **Safe mode:** call `clipfit_preview` with one or more structured operations,
  verify match counts, inspect every returned hunk, then call `clipfit_apply` with
  the `preview_id`. If policy requires human approval, stop after preview and wait
  for the user before apply. Apply returns a compact receipt and does not repeat
  the preview hunks.
- **Direct mode:** call `clipfit_edit` with the same `path` and `operations` when
  policy permits a low-risk, unambiguous edit without a pre-write review round
  trip. Direct mode returns a compact receipt without hunks.

For either mode, prefer `replace_block` with a unique verbatim anchor above find.
The anchor is optional; to edit the beginning of a file, omit it and use a unique
multi-line `find` block with `expected_matches: 1`. Use `replace` only when both
`find` and `replace` are single lines with no CR/LF; use `replace_block` for every
multi-line edit. Direct mode skips preview review, but it keeps exact match
validation, root and symlink checks, a same-call stale-content check, backup
creation, and atomic write.

For a new file, call `clipfit_create`; it refuses to overwrite an existing file or
symlink. If an applied edit is wrong, call `clipfit_rollback` immediately. Backups
are short-lived and keep only the most recent apply for each file.

Distant edits remain localized even when an earlier operation changes line
counts. Keep related operations in one preview. Split them only when ClipFit
returns an explicit response safety-limit error.

The server enforces exact match counts, unique anchors, single-use preview tokens,
file-content checks between preview and apply, and a mandatory root boundary.
These guarantees are server-side and do not depend on model compliance.

## Select a mode by policy and risk

Do not choose direct mode merely because a model is assumed to be capable. Use
observable risk, reversibility, edit scope, and available verification.

Use direct mode only when all of these are true:

- The closest `AGENTS.md` permits it.
- The change is small, localized, and easy to reverse.
- Matching is exact and unambiguous, normally with one unique anchor or
  `expected_matches: 1`.
- Relevant formatting, tests, or another strong post-write check is available.

Use safe mode when any of these apply:

- The edit affects authentication, security, money, concurrency, persistence,
  recovery, protocol/wire formats, schemas, migrations, infrastructure, or deploys.
- It changes a public API, spans coupled files, deletes substantial logic, performs
  a broad rename/swap, or intentionally matches multiple locations.
- Intent is unclear, tests are weak or unavailable, project policy requires
  preview, or the user asks to review before writing.

Treat server validation, agent hunk review, explicit human approval, and post-write
build/tests as separate safety layers. An agent inspecting preview hunks is not
human-in-the-loop unless it pauses for a person. A nested `AGENTS.md` may tighten
or relax a broader policy for its subtree.

## CLI compatibility

The binary also retains the legacy spec-file CLI. Preview with
`clipfit apply --dry-run <target> <spec>`, apply with
`clipfit apply <target> <spec>`, and restore with `clipfit rollback <target>`.
Add `--json` for a machine-readable result on stdout; human diagnostics go to
stderr. Exit codes are 0 for success, 1 for an unmatched command, and 2 for an
I/O or usage error.

## Legacy CLI spec format

One spec file may contain many blocks; they are applied top to bottom, each
seeing the result of the previous. An optional first line `===CLIPFIT: <model> ===`
is allowed and ignored by the CLI.

### REPLACE — single-line, literal substring, global

Replaces every occurrence of the exact text. No word boundaries. Both sides must
be single lines with no CR/LF; invalid commands are rejected before backup or
write. Use REPLACE_BLOCK for multi-line edits.

```
===REPLACE===
old text on one line
===WITH===
new text on one line
===END_REP===
```

### REPLACE_BLOCK — multi-line block

The find block MUST be an exact copy of the source lines (indentation and
blank lines included). Leading common indentation is normalized, so you may
paste the block dedented, but the relative shape must match exactly.

```
===REPLACE_BLOCK===
<exact original lines>
===WITH===
<replacement lines>
===END_REP===
```

When the target block is repeated, pin it with a unique verbatim anchor that
appears above the target. The target search starts after the anchor and replaces
only the first matching block:

```
===REPLACE_BLOCK===
===ANCHOR===
<one unique source line or contiguous block above the target>
===TARGET===
<exact original target lines>
===WITH===
<replacement lines>
===END_REP===
```

The anchor and target do not need to be adjacent. If the anchor itself is not
unique, copy a larger contiguous anchor block.

### SWAP_NAME — whole-word identifier swap (both directions)

Swaps every whole-word occurrence of A with B and B with A in one atomic pass.
Whole-word means `\b` boundaries: `event` is swapped, `eventHandler` is not;
`event.id` and `event[i]` ARE swapped (the `.`/`[` ends the word). Case-sensitive.

```
===SWAP_NAME===
nameA
===WITH===
nameB
===END_SWP===
```

## Choosing the right command

- Renaming or swapping an identifier (variable/function/type name) → SWAP_NAME
  (or, for a one-directional rename, a REPLACE_BLOCK/REPLACE with enough context).
- Replacing a literal substring or string content (`"old"`, `obj.method`, a
  token containing `$`/`@`) → REPLACE. SWAP_NAME's `\b` does not work on
  symbol-prefixed targets.
- Replacing a contiguous chunk of code → REPLACE_BLOCK.

## Gotchas

- REPLACE is a single-line literal substring replace with NO word boundary.
  CR/LF on either side is rejected. `event` will also hit `prevent`,
  `eventListener`, `myEvent`. For multi-line edits use REPLACE_BLOCK; for
  identifier renames use SWAP_NAME or add surrounding context to disambiguate.
- The legacy CLI reports zero matches when a REPLACE_BLOCK find does not match.
  Always inspect the report and re-copy the exact source lines before retrying.
- SWAP_NAME is whole-word and case-sensitive; it cannot target identifiers that
  start with a non-word character (`$x`, `@x`).
- In safe mode, inspect every MCP preview hunk before apply and pause if human
  approval is required. Direct mode returns only a compact post-write receipt.
  Legacy CLI reports are complete, while MCP previews fail closed if the encoded
  response would exceed the safety limit.
- Backups live in the system temp directory and are short-lived; rollback is
  only guaranteed right after apply, not days later.

## Notes

- MCP clients launch the configured ClipFit server and discover its tools.
- The legacy CLI expects `clipfit` on PATH or an explicit binary path.
- CLI invocations do one thing and exit; there is no interactive prompt.