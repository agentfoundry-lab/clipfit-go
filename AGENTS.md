# ClipFit project editing policy

## Required editor

- Use ClipFit MCP for every modification to existing project files.
- Use `clipfit_create` for new files; it must refuse to overwrite an existing path.
- If ClipFit is unavailable, cannot reach the target, or cannot produce a safely
  validated edit, stop project-file editing and report the blocker. Do not fall
  back to another editor.

## Choose the workflow by risk

Direct `clipfit_edit` is permitted only for small, localized documentation or
isolated test changes when all matches are exact and unambiguous, the change is
easy to reverse, and a relevant post-write check is available.

Use `clipfit_preview`, inspect every hunk, then `clipfit_apply` for:

- Changes to editing semantics, MCP schemas or responses, transport, filesystem
  boundaries, symlink handling, stale-content detection, backups, rollback, or
  atomic writes.
- Public CLI behavior, protocol compatibility, concurrency, or reliability logic.
- Broad renames or swaps, multi-file coupled changes, substantial deletions, or
  any operation intentionally matching more than one location.
- Unclear intent, weak verification, or any user request to review before writing.

If a task explicitly requires human approval, stop after preview and wait for the
user. Agent inspection of hunks alone is not human approval.

## Verification

- Run `gofmt` on changed Go files.
- Run `go test ./...` after code changes.
- Check Markdown links and examples after documentation changes.
- Inspect the final diff when Git metadata is available.
