# WSL project editing

## Required mechanism

- When modifying source code or project files inside WSL, use ClipFit MCP.
- Read the closest applicable project or subtree `AGENTS.md` before editing and
  follow its safe/direct mode policy.
- Use `clipfit_create` for new files; it must not overwrite an existing path.
- Treat the server's configured root as an immutable maximum scope. Use a path
  inside it and never try to widen the root as an editing workaround.

## Select the existing-file workflow

A closer `AGENTS.md` controls the workflow for its scope. When no closer policy
exists, use direct `clipfit_edit` only when all of these are true:

- The change is small, localized, low impact, and easy to reverse.
- Matching is exact and unambiguous, normally with a unique anchor or
  `expected_matches: 1`.
- Relevant formatting, tests, or another strong post-write check is available.

Use `clipfit_preview`, inspect every returned hunk, then `clipfit_apply` when any
of these are true:

- The edit affects authentication, security, money, concurrency, persistence,
  recovery, protocol/wire formats, schemas, migrations, infrastructure, deploys,
  or a public API.
- It spans coupled files, deletes substantial logic, performs a broad rename or
  swap, intentionally matches multiple locations, or is difficult to reverse.
- Intent is unclear, verification is weak, project policy requires preview, or
  the user asks to review before writing.

If human approval is required, stop after preview and wait for the user. Agent
inspection of preview hunks alone is not human approval.

## Failure and verification

- Run relevant formatting and tests after either workflow.
- If ClipFit MCP is unavailable, cannot access the target, or cannot produce a
  safely validated edit, stop all project-file editing and report the blocker.
- Do not fall back to `apply_patch`, `git apply`, shell redirection, Python
  file-writing scripts, or another project-file editor when ClipFit is required.
- Read-only inspection, builds, tests, and read-only Git commands may continue.
  Normal Git staging and commits remain allowed when the user requests them;
  Git must not be used as a fallback file editor.
