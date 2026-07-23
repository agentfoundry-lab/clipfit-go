# ClipFit Go / MCP

ClipFit is a policy-selectable structured text editor designed for LLM agents.
It retains the original CLI and also runs as a local MCP stdio server. Windows
Codex can invoke it through `wsl.exe` to modify or create WSL files without
editing through `\\wsl$` paths.

## Origin: ClipFit for VS Code

ClipFit originated as the [ClipFit extension for Visual Studio Code](https://marketplace.visualstudio.com/items?itemName=AgentFoundry.clipfit), created
by Frank Fu and published by AgentFoundry. The extension introduced a
protocol-driven workflow in which an LLM emits structured `REPLACE`,
`REPLACE_BLOCK`, or `SWAP_NAME` commands and the developer reviews the resulting
changes in VS Code's native Diff view before accepting or rejecting them.

ClipFit Go ports that structured editing core to a standalone Go CLI and local MCP
server for headless agent workflows. It keeps compatibility with the original
text protocol while adding server-enforced filesystem boundaries, exact-match
validation, transactional preview/apply, optional direct editing, backups, and
atomic writes.

## Project highlights

- **Guarded when correctness matters:** safe mode computes the exact localized
  hunks before writing, lets an agent or human review them, and binds apply to
  the reviewed file content with a short-lived, single-use `preview_id`.
- **Fast when risk is low:** direct mode applies the same structured operations
  in one call while retaining match-count, root, symlink, race, backup, and
  atomic-write checks.
- **Policy-selectable:** teams can choose safe or direct mode globally, per
  repository, or per subtree with `AGENTS.md` instead of paying the same review
  cost for every change.
- **Fail-closed matching:** exact counts and unique anchors reject missing or
  ambiguous edits before they reach disk.
- **Compact MCP responses:** preview returns hunks once; successful writes return
  concise receipts rather than echoing the patch again.

## MCP tools

- `clipfit_preview`: safe-mode planning; validates structured operations and
  returns localized hunks plus a short-lived `preview_id` without writing.
- `clipfit_apply`: safe-mode commit; accepts only a `preview_id`, applies the
  exact reviewed result, and returns a compact receipt without repeating hunks.
- `clipfit_edit`: direct mode; validates and applies structured operations in one
  call, then returns a compact receipt. It skips pre-write diff review, not the
  match-count, root, symlink, race, backup, or atomic-write checks.
- `clipfit_create`: creates a new file and optional parent directories; it never
  overwrites an existing path or symlink and does not echo the complete file content.
- `clipfit_rollback`: restores the most recent apply backup for one file.

Distant edits remain separate localized hunks even when an earlier edit changes
the total line count. A normal preview can contain multiple operations; do not
split requests merely because stdio pipes commonly have a 64 KiB capacity. That
capacity is not a JSON-RPC or MCP message-size limit. `content.text` contains only
a summary, while the complete result appears once in `structuredContent`.

The server enforces a 256 KiB safety limit on each encoded JSON-RPC response.
Split edits into smaller previews only after an explicit `response safety limit`
error. A rejected preview never issues or retains a `preview_id` and never writes.

The MCP server's `--root` is a mandatory security boundary. Relative paths are
resolved from that root, while absolute paths, `..`, and symlinks are checked to
prevent escapes.

Existing files support two workflows. Choose between them from the closest
project policy and the edit's blast radius, reversibility, and available
verification—not from an assumption that a particular model is "smart enough."

Safe mode is appropriate when a diff should be reviewed before writing:

1. Call `clipfit_preview`; every operation must match safely.
2. Inspect every returned hunk.
3. If project policy requires human approval, stop and wait for it.
4. Pass the `preview_id` to `clipfit_apply`.

Preview tokens expire after ten minutes and are single-use. A server restart,
source-file change, or previously used token causes apply to refuse the write and
require a new preview. A successful apply returns only a compact receipt because
the hunks were already returned by preview.

Direct mode calls `clipfit_edit` with the same `path` and `operations`. It plans and
writes in one server call without returning pre-write hunks. Use it for low-risk,
unambiguous edits when the extra review round trip is unnecessary. Direct mode
still fails closed on unexpected match counts and creates a rollback backup before
an atomic write.

Safety is layered: server-side validation, agent review, explicit human approval,
and post-write formatting/tests are separate controls. Preview inspected by an
agent is pre-write review; it becomes human-in-the-loop only when the policy tells
the agent to wait for a person before apply. See [Agent editing policies](docs/agent-policy.md)
for ready-to-use global, repository, and subtree examples.

## Build and test

```bash
cd ~/clipfit-go
go test ./...
go build -buildvcs=false -trimpath -o ~/.local/bin/clipfit .
```

Start the stdio server:

```bash
~/.local/bin/clipfit mcp --root /home/user
```

stdout is reserved for MCP JSON-RPC. Diagnostics go to stderr and never enter the protocol stream.

### MCP diagnostics and backpressure

The server emits one JSON telemetry record per line on stderr. Records include
request IDs, methods and tools, operation and input byte counts, phase durations,
hunk statistics, response bytes, and encode/write/flush lifecycle events. A bounded
queue ensures that a blocked stderr consumer can only drop diagnostics, not block JSON-RPC.

MCP responses are fully encoded and size-checked in memory before reaching stdout.
If the client does not drain the response for 15 seconds, the server closes the
transport and exits so Codex can restart it instead of hanging forever in write or flush.

## Install in Windows Codex

Add the following to the user-level Codex `~/.codex/config.toml`, normally
`C:\Users\<user>\.codex\config.toml` on Windows:

```toml
[mcp_servers.clipfit]
command = "wsl.exe"
args = ["-d", "Ubuntu-24.04", "--", "/home/user/.local/bin/clipfit", "mcp", "--root", "/home/user"]
startup_timeout_sec = 15
tool_timeout_sec = 60
```

Restart Codex after saving. Confirm in MCP server settings or `/mcp` that
`clipfit_preview`, `clipfit_apply`, `clipfit_edit`, `clipfit_create`, and
`clipfit_rollback` are available.

To narrow the writable scope, change the final `/home/user` argument to one
repository, such as `/home/user/my-project`.

### Install the optional Codex skill

Copy `SKILL.md` and `agents/openai.yaml` into a user skill directory:

```text
C:\Users\<user>\.codex\skills\clipfit\SKILL.md
C:\Users\<user>\.codex\skills\clipfit\agents\openai.yaml
```

The skill teaches Codex both workflows and defers the mode choice to the closest
applicable `AGENTS.md`. Restart Codex after installing or updating it.

## Structured MCP operations

Prefer `replace_block` for normal edits. If find text may repeat, provide a
verbatim `anchor` consisting of one unique line or block above the find text:

```json
{
  "path": "/home/user/project/main.go",
  "operations": [
    {
      "type": "replace_block",
      "anchor": "func runServer() error {",
      "find": "timeout := 30 * time.Second",
      "replace": "timeout := 60 * time.Second"
    }
  ]
}
```

Rules:

- For `replace_block`, the anchor must be unique; the first find after the anchor is selected.
- `anchor` is optional. To edit the beginning of a file, omit it and use a
  sufficiently large unique `find` block with `expected_matches: 1`.
- `replace` is strictly single-line: both `find` and `replace` must contain no
  CR/LF. Use `replace_block` for every multi-line edit.
- Unanchored `replace_block` and `replace` default to exactly one match.
- Set `expected_matches` only when multiple locations are intentionally changed.
- `swap_name` performs one atomic two-way swap of whole-word identifiers.
- Operations run sequentially, and each one sees the previous preview result.
- The entire preview fails if any operation is missing, ambiguous, or produces no change.

After preview succeeds and every hunk has been inspected, pass this argument to `clipfit_apply`:

```json
{"preview_id": "<clipfit_preview returned id>"}
```

To skip pre-write review, pass the original `path` and `operations` directly to
`clipfit_edit`. Both workflows apply operations sequentially and enforce the same
match-count rules; only safe mode returns hunks before writing.

## Legacy CLI patch format

```text
===REPLACE_BLOCK===
===ANCHOR===
func runServer() error {
===TARGET===
exact old block
===WITH===
replacement block
===END_REP===
```

The anchor must appear above the target, but they need not be adjacent. With an
anchor, only the first subsequent target is replaced. Legacy `REPLACE` remains a
global literal operation, but both sides must be single lines; invalid multi-line
commands are rejected before backup or write. CLI `apply --dry-run`, direct apply,
and rollback remain backward compatible.

## License

Copyright (c) 2026 Frank Fu (AgentFoundry).

ClipFit Go is licensed under the [MIT License](LICENSE).
