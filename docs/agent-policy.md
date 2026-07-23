# Policy-selectable editing with ClipFit

ClipFit separates server-enforced write safety from the amount of review a project
requires. Teams can select the workflow globally, per repository, or for a nested
subtree with `AGENTS.md`.

## Two workflows

| Workflow | Tool sequence | Best fit |
| --- | --- | --- |
| Guarded | `clipfit_preview` → inspect hunks → `clipfit_apply` | High-impact, coupled, difficult-to-reverse, or explicitly reviewed changes |
| Direct | `clipfit_edit` | Small, exact, reversible changes with strong post-write verification |

Both workflows enforce the configured root, path and symlink boundaries, exact
match counts, stale-content checks, backup creation, and atomic writes. Guarded
mode additionally exposes the server-computed hunks before writing and binds apply
to the reviewed file content with a short-lived, single-use token.

## Root and privilege model

Start the MCP server with an explicit `--root`; it refuses to start without one.
The resolved root is the immutable maximum scope for that server process, so an
agent tool call cannot widen it. A project root provides stronger isolation. A
home-directory root supports cross-project edits through root-relative paths but
also exposes more user files and should be a deliberate trust decision.

ClipFit supplies a narrow structured-write capability, so the agent's general
shell can remain sandboxed without `danger-full-access` or broad filesystem write
permission. This is least-privilege composition, not a sandbox bypass: the
ClipFit process still requires normal operating-system access to its configured
root.

## Safety layers

Treat these as independent controls:

1. **Server validation:** root, path, symlink, matching, race, backup, and atomic-write checks.
2. **Agent pre-write review:** the model inspects every preview hunk before apply.
3. **Human approval:** the agent pauses after preview and waits for a person.
4. **Post-write verification:** formatters, linters, builds, tests, and final diff review.

Preview does not automatically imply human-in-the-loop. A policy must explicitly
require the agent to wait when human approval is necessary.

## Recommended decision rule

Allow direct mode only when all of these are true:

- The closest applicable policy permits it.
- The change is small, localized, reversible, and low impact.
- A unique anchor or `expected_matches: 1` makes matching unambiguous.
- Relevant formatting, tests, or another strong check can run after writing.

Require guarded mode when any of these are true:

- The change affects authentication, security, money, concurrency, persistence,
  recovery, protocol/wire formats, schemas, migrations, infrastructure, or deploys.
- It changes a public API, spans coupled files, deletes substantial logic, performs
  a broad rename/swap, or intentionally matches multiple locations.
- Intent is unclear, verification is weak, project policy requires preview, or the
  user asks to review before writing.

Base the choice on observable risk and verification—not on whether a model is
assumed to be "smart enough."

## Global `AGENTS.md` example

A complete Windows/WSL template is available at
[`examples/windows-global-AGENTS.md`](../examples/windows-global-AGENTS.md).

```md
# WSL project editing

- Use ClipFit MCP for WSL project files.
- Follow the closest project `AGENTS.md` for safe/direct mode policy.
- When no closer policy exists, use `clipfit_edit` only for small, exact,
  low-risk, reversible edits with strong post-write verification.
- Use `clipfit_preview`, inspect every hunk, then `clipfit_apply` for broad,
  security-sensitive, concurrent, persistent, protocol, migration, public API,
  or difficult-to-reverse changes.
- If human approval is required, stop after preview and wait for the user.
- Use `clipfit_create` for new files.
- Run relevant formatting and tests after either workflow.
- If ClipFit cannot safely perform the edit, stop and report the blocker; do not
  fall back to another project-file editor.
```

## Repository or subtree examples

A strict core directory can contain a nested `AGENTS.md`:

```md
- Changes in this subtree require `clipfit_preview` followed by `clipfit_apply`.
- Broad rename and `swap_name` operations always require preview.
- Stop after preview when the task requires human approval.
```

A lower-risk documentation subtree can permit direct mode:

```md
- `clipfit_edit` is the default for exact, single-match documentation edits.
- Escalate to preview/apply for broad replacements, generated references, or
  changes whose correctness cannot be checked locally.
```

Closer policies should express the risk tolerance of their subtree while retaining
ClipFit's required server-side safeguards.
