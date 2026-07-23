// ClipFit CLI is a language-independent batch editor designed for direct use
// by coding agents and command-line workflows.
//
// Subcommands:
//
//	clipfit apply    [--dry-run] [--json] <target-file> <spec-file>   Apply edits and create a backup
//	clipfit rollback [--json] <target-file>                           Restore the most recent backup
//	clipfit clean    [--json]                                         Remove all ClipFit backups
//
// The first positional argument is the target file. The second is a ClipFit
// instruction file. Keeping the edit payload in a file prevents shells from
// corrupting unusual bytes or escaping multi-line content.
//
// apply returns a complete, untruncated change report so an LLM can verify every
// changed line. Backups are keyed by the target's absolute-path hash and stored
// in the system temporary directory without touching the repository. They are
// intentionally short-lived because the operating system may clean that directory.
//
// Unlike the original editor integration, the CLI has no UI for REMARKNCOLOR.
// Remark tags are stripped safely instead of being inserted into source files.
//
// Supported commands are REPLACE (single-line global literal text),
// REPLACE_BLOCK (a code block), and SWAP_NAME (an atomic whole-word identifier
// swap). All commands are language independent. SWAP_NAME uses \b boundaries, so
// symbol-prefixed names
// such as $x or @x should instead use a contextual REPLACE operation.
//
// parseKissCommands and applyCommands were ported carefully from the original
// TypeScript implementation, including the trailing greedy-\s+ fix in
// buildFlexPattern.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// ClipFit protocol tags
const (
	tagReplaceBlock = "===REPLACE_BLOCK==="
	tagReplace      = "===REPLACE==="
	tagSwapName     = "===SWAP_NAME===" // Keep this literal in sync with TAGS.SWAP_NAME in constants.ts
	tagAnchor       = "===ANCHOR==="
	tagTarget       = "===TARGET==="
	tagWith         = "===WITH==="
	tagEndRep       = "===END_REP==="
	tagEndSwp       = "===END_SWP===" // Keep this literal in sync with TAGS.END_SWP in constants.ts
	tagRemark       = "===REMARKNCOLOR==="
	tagRemarkEnd    = "===REMARK_END==="
)

type Command struct {
	Type       string // "REPLACE", "REPLACE_BLOCK", or "SWAP_NAME"
	AnchorStr  string
	FindStr    string
	ReplaceStr string
}

type CmdStat struct {
	Index   int    `json:"index"`
	Type    string `json:"type"`
	Matches int    `json:"matches"` // Number of replaced locations
}

// Match whitespace runs, including NBSP, to mirror the JavaScript implementation's \s collapsing behavior.
var wsRun = regexp.MustCompile("[\t\n\f\r \u00A0]+")

func validateSingleLineReplace(find, replacement string) error {
	if strings.ContainsAny(find, "\r\n") || strings.ContainsAny(replacement, "\r\n") {
		return fmt.Errorf("REPLACE supports single-line find and replacement only; use REPLACE_BLOCK for multi-line edits")
	}
	return nil
}

func validateCommands(commands []Command) error {
	for index, command := range commands {
		if command.Type != "REPLACE" {
			continue
		}
		if err := validateSingleLineReplace(command.FindStr, command.ReplaceStr); err != nil {
			return fmt.Errorf("command #%d: %w", index+1, err)
		}
	}
	return nil
}

// parseKissCommands parses an instruction file into commands using the original TypeScript state machine.
func parseKissCommands(text string) []Command {
	var commands []Command
	lines := strings.Split(text, "\n")

	state := "OUTSIDE"
	cmdType := ""
	var anchorLines, findLines, replaceLines []string

	flush := func() {
		if state == "IN_REPLACE" && cmdType != "" {
			commands = append(commands, Command{
				Type:       cmdType,
				AnchorStr:  strings.Join(anchorLines, "\n"),
				FindStr:    strings.Join(findLines, "\n"),
				ReplaceStr: strings.Join(replaceLines, "\n"),
			})
		}
	}

	for _, line := range lines {
		cleanLine := strings.TrimRightFunc(line, unicode.IsSpace) // Normalize only for tag matching; preserve the original line content.

		switch cleanLine {
		case tagEndRep, tagEndSwp:
			flush()
			state = "OUTSIDE"
			continue
		case tagReplaceBlock, tagReplace, tagSwapName:
			flush()
			switch cleanLine {
			case tagReplaceBlock:
				cmdType = "REPLACE_BLOCK"
			case tagSwapName:
				cmdType = "SWAP_NAME"
			default:
				cmdType = "REPLACE"
			}
			state = "IN_FIND"
			anchorLines = nil
			findLines = nil
			replaceLines = nil
			continue
		}

		if cmdType == "REPLACE_BLOCK" && (state == "IN_ANCHOR" || state == "IN_FIND") {
			switch cleanLine {
			case tagAnchor:
				state = "IN_ANCHOR"
				anchorLines = nil
				continue
			case tagTarget:
				state = "IN_FIND"
				findLines = nil
				continue
			}
		}
		if state == "OUTSIDE" {
			continue
		}
		if state == "IN_ANCHOR" {
			if cleanLine == tagWith {
				state = "IN_REPLACE"
			} else {
				anchorLines = append(anchorLines, line)
			}
			continue
		}
		if state == "IN_FIND" {
			if cleanLine == tagWith {
				state = "IN_REPLACE"
			} else {
				findLines = append(findLines, line)
			}
			continue
		}
		if state == "IN_REPLACE" {
			replaceLines = append(replaceLines, line)
			continue
		}
	}
	flush() // Accept an instruction file that ends in IN_REPLACE without END_REP.
	return commands
}

// dedent removes common indentation and surrounding blank lines, matching the original dedentCode.
func dedent(text string) string {
	lines := strings.Split(text, "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	minIndent := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := 0
		for n < len(l) && (l[n] == ' ' || l[n] == '\t') {
			n++
		}
		if minIndent == -1 || n < minIndent {
			minIndent = n
		}
	}
	if minIndent < 0 {
		minIndent = 0
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			out[i] = ""
		} else {
			out[i] = l[minIndent:] // The first minIndent bytes are guaranteed to be ASCII whitespace.
		}
	}
	return strings.Join(out, "\n")
}

// buildFlexPattern creates a whitespace-tolerant matcher from a find block.
// Drop an unanchored trailing greedy \s+ so it cannot consume the following newline and indentation.
func buildFlexPattern(cleanFind string) string {
	escaped := regexp.QuoteMeta(cleanFind) // Use the same escaped character set as the TypeScript implementation.
	flex := wsRun.ReplaceAllLiteralString(escaped, `\s+`)
	for strings.HasSuffix(flex, `\s+`) { // Whitespace runs are collapsed, so this normally executes once.
		flex = strings.TrimSuffix(flex, `\s+`)
	}
	return `(^|\n)([ \t]*)(` + flex + `)`
}

// alignReplace applies the matched indentation to each replacement line and strips remark tags.
func alignReplace(cleanReplace, leadingIndent string) string {
	var out []string
	for _, line := range strings.Split(cleanReplace, "\n") {
		if strings.HasPrefix(line, tagRemark) || strings.HasPrefix(strings.TrimSpace(line), tagRemarkEnd) {
			continue // Never insert remark metadata as source code.
		}
		if line == "" {
			out = append(out, "")
			continue
		}
		out = append(out, leadingIndent+line)
	}
	return strings.Join(out, "\n")
}

// applyCommands applies commands sequentially and returns the updated text plus per-command match counts.
func applyCommands(originalText string, commands []Command) (string, []CmdStat) {
	finalCode := originalText
	stats := make([]CmdStat, 0, len(commands))

	for i, cmd := range commands {
		if cmd.FindStr == "" {
			stats = append(stats, CmdStat{i, cmd.Type, 0})
			continue
		}

		switch cmd.Type {
		case "REPLACE_BLOCK":
			cleanFind := dedent(cmd.FindStr)
			cleanReplace := dedent(cmd.ReplaceStr)
			if cleanFind == "" {
				stats = append(stats, CmdStat{i, cmd.Type, 0})
				continue
			}
			re, err := regexp.Compile(buildFlexPattern(cleanFind))
			if err != nil {
				fmt.Fprintf(os.Stderr, "  command #%d pattern failed to compile: %v\n", i+1, err)
				stats = append(stats, CmdStat{i, cmd.Type, 0})
				continue
			}
			matches := re.FindAllStringSubmatchIndex(finalCode, -1)
			cleanAnchor := dedent(cmd.AnchorStr)
			if cleanAnchor != "" {
				anchorRE, err := regexp.Compile(buildFlexPattern(cleanAnchor))
				if err != nil {
					stats = append(stats, CmdStat{i, cmd.Type, 0})
					continue
				}
				anchorMatch := anchorRE.FindStringSubmatchIndex(finalCode)
				if anchorMatch == nil {
					stats = append(stats, CmdStat{i, cmd.Type, 0})
					continue
				}
				searchStart := anchorMatch[1]
				targetMatch := re.FindStringSubmatchIndex(finalCode[searchStart:])
				if targetMatch == nil {
					stats = append(stats, CmdStat{i, cmd.Type, 0})
					continue
				}
				adjusted := make([]int, len(targetMatch))
				for j, offset := range targetMatch {
					adjusted[j] = offset
					if offset >= 0 {
						adjusted[j] += searchStart
					}
				}
				matches = [][]int{adjusted}
			}
			if len(matches) == 0 {
				stats = append(stats, CmdStat{i, cmd.Type, 0})
				continue
			}
			var b strings.Builder
			last := 0
			for _, m := range matches {
				b.WriteString(finalCode[last:m[0]]) // Preserve source text before the match.
				g1 := ""
				if m[2] >= 0 {
					g1 = finalCode[m[2]:m[3]] // (^|\n): start of input or a newline.
				}
				g2 := ""
				if m[4] >= 0 {
					g2 = finalCode[m[4]:m[5]] // [ \t]*: matched indentation.
				}
				b.WriteString(g1)
				if cleanReplace != "" {
					b.WriteString(alignReplace(cleanReplace, g2))
				}
				last = m[1]
			}
			b.WriteString(finalCode[last:])
			finalCode = b.String()
			stats = append(stats, CmdStat{i, cmd.Type, len(matches)})

		case "REPLACE":
			if err := validateSingleLineReplace(cmd.FindStr, cmd.ReplaceStr); err != nil {
				stats = append(stats, CmdStat{i, cmd.Type, 0})
				continue
			}
			cleanFind := strings.ReplaceAll(cmd.FindStr, "\u00A0", " ")
			count := strings.Count(finalCode, cleanFind)
			finalCode = strings.ReplaceAll(finalCode, cleanFind, cmd.ReplaceStr)
			stats = append(stats, CmdStat{i, cmd.Type, count})

		case "SWAP_NAME":
			// Perform an atomic swap with one global \b(A|B)\b expression. The
			// callback returns the opposite name for each match. The regex cursor moves
			// past every replacement immediately, so a swapped value cannot be matched
			// again. Word boundaries avoid changing eventHandler while still matching
			// event in event.id or event[i]. This intentionally preserves the original
			// SWAP_NAME limitation for symbol-prefixed identifiers.
			nameA := strings.TrimSpace(strings.ReplaceAll(cmd.FindStr, "\u00A0", " "))
			nameB := strings.TrimSpace(strings.ReplaceAll(cmd.ReplaceStr, "\u00A0", " "))
			if nameA == "" || nameB == "" || nameA == nameB {
				stats = append(stats, CmdStat{i, cmd.Type, 0})
				continue
			}
			re, err := regexp.Compile(`\b(` + regexp.QuoteMeta(nameA) + `|` + regexp.QuoteMeta(nameB) + `)\b`)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  command #%d SWAP pattern failed to compile: %v\n", i+1, err)
				stats = append(stats, CmdStat{i, cmd.Type, 0})
				continue
			}
			count := len(re.FindAllString(finalCode, -1))
			finalCode = re.ReplaceAllStringFunc(finalCode, func(m string) string {
				if m == nameA {
					return nameB
				}
				return nameA
			})
			stats = append(stats, CmdStat{i, cmd.Type, count})
		}
	}
	return finalCode, stats
}

// preprocessSpec normalizes newlines and converts NBSP to regular spaces at line edges.
func preprocessSpec(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = normalizeNBSPEdges(line)
	}
	return strings.Join(lines, "\n")
}

func normalizeNBSPEdges(line string) string {
	r := []rune(line)
	for i := 0; i < len(r) && unicode.IsSpace(r[i]); i++ {
		if r[i] == '\u00A0' {
			r[i] = ' '
		}
	}
	for i := len(r) - 1; i >= 0 && unicode.IsSpace(r[i]); i-- {
		if r[i] == '\u00A0' {
			r[i] = ' '
		}
	}
	return string(r)
}

// fileMeta records target-file properties that must be restored when writing.
type fileMeta struct {
	hadBOM bool
	crlf   bool
	mode   os.FileMode
}

// decodeTarget removes a BOM, normalizes newlines, and records the original file format.
func decodeTarget(data []byte, path string) (string, fileMeta) {
	var meta fileMeta
	if info, e := os.Stat(path); e == nil {
		meta.mode = info.Mode()
	} else {
		meta.mode = 0644
	}
	bom := []byte{0xEF, 0xBB, 0xBF}
	if bytes.HasPrefix(data, bom) {
		meta.hadBOM = true
		data = data[3:]
	}
	s := string(data)
	meta.crlf = strings.Contains(s, "\r\n")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return s, meta
}

// renderOutput restores the original BOM and newline style without changing trailing-newline presence.
func renderOutput(content string, meta fileMeta) []byte {
	out := content
	if meta.crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	b := []byte(out)
	if meta.hadBOM {
		b = append([]byte{0xEF, 0xBB, 0xBF}, b...)
	}
	return b
}

// writeAtomic writes a temporary file in the target directory and renames it into place.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".clipfit-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if mode != 0 {
		os.Chmod(tmpName, mode)
	}
	return os.Rename(tmpName, path)
}

// ---------------- Change reporting (line-based diff) ----------------

// Hunk describes one change region: its original start line, removed lines, and added lines.
type Hunk struct {
	OldStart int      `json:"old_start"` // 1-based line number in the original file
	Removed  []string `json:"removed"`
	Added    []string `json:"added"`
}

// ---------------- Backups (flat hash names in system temp) ----------------

// backupPath uses the SHA-256 of the absolute path as a flat filename under <temp>/clipfit/.
// Full paths prevent same-name collisions, and each target keeps one rollback generation.
func backupPath(absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return filepath.Join(os.TempDir(), "clipfit", hex.EncodeToString(sum[:])+".bak")
}

// writeBackup stores the original bytes verbatim and replaces the previous backup for that target.
func writeBackup(absPath string, rawData []byte) (string, error) {
	bp := backupPath(absPath)
	if err := os.MkdirAll(filepath.Dir(bp), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(bp, rawData, 0600); err != nil {
		return "", err
	}
	return bp, nil
}

// ---------------- Result types ----------------

type Result struct {
	Action      string    `json:"action"`
	Target      string    `json:"target"`
	OK          bool      `json:"ok"`
	Commands    int       `json:"commands,omitempty"`
	Stats       []CmdStat `json:"stats,omitempty"`
	ChangeCount int       `json:"change_count"`
	Hunks       []Hunk    `json:"hunks,omitempty"`
	BackupPath  string    `json:"backup_path,omitempty"`
	Message     string    `json:"message,omitempty"`
}

func printJSON(res Result) {
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
}

func printChangeReport(stats []CmdStat, hunks []Hunk) {
	for _, st := range stats {
		note := ""
		if st.Matches == 0 {
			note = "  <- no matches"
		}
		fmt.Fprintf(os.Stderr, "  command #%d (%s): %d match(es)%s\n", st.Index+1, st.Type, st.Matches, note)
	}
	fmt.Fprintf(os.Stderr, "Changed %d hunk(s); the complete report follows:\n", len(hunks))
	for _, h := range hunks {
		for j, r := range h.Removed {
			fmt.Fprintf(os.Stderr, "  -[line %d] %s\n", h.OldStart+j, r)
		}
		for _, a := range h.Added {
			fmt.Fprintf(os.Stderr, "  +        %s\n", a)
		}
	}
}

// ---------------- Subcommands ----------------

func runApply(target, spec string, dryRun, asJSON bool) int {
	abs, err := filepath.Abs(target)
	if err != nil {
		abs = target
	}
	rawData, err := os.ReadFile(target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read target file: %v\n", err)
		return 2
	}
	content, meta := decodeTarget(rawData, target)

	specRaw, err := os.ReadFile(spec)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read instruction file: %v\n", err)
		return 2
	}
	commands := parseKissCommands(preprocessSpec(string(specRaw)))
	if len(commands) == 0 {
		fmt.Fprintln(os.Stderr, "no valid ClipFit commands found")
		return 1
	}
	if err := validateCommands(commands); err != nil {
		fmt.Fprintf(os.Stderr, "invalid ClipFit command: %v\n", err)
		return 2
	}

	result, stats := applyCommands(content, commands)
	hunks := computeChanges(content, result)
	missed := 0
	for _, st := range stats {
		if st.Matches == 0 {
			missed++
		}
	}

	res := Result{
		Action: "apply", Target: abs, Commands: len(commands),
		Stats: stats, ChangeCount: len(hunks), Hunks: hunks, OK: missed == 0,
	}

	if dryRun {
		res.Message = "dry-run: file and backup were not changed"
		if asJSON {
			printJSON(res)
		} else {
			printChangeReport(stats, hunks)
			fmt.Fprintln(os.Stderr, "(dry-run: file and backup were not changed; complete preview is on stdout)")
			os.Stdout.Write(renderOutput(result, meta))
		}
		if missed > 0 {
			return 1
		}
		return 0
	}

	// Refuse to apply without a backup so the rollback safety net is always available.
	bp, err := writeBackup(abs, rawData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create backup; apply aborted to preserve rollback capability: %v\n", err)
		return 2
	}
	res.BackupPath = bp

	if err := writeAtomic(target, renderOutput(result, meta), meta.mode); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write target file: %v\n", err)
		return 2
	}

	if asJSON {
		printJSON(res)
	} else {
		printChangeReport(stats, hunks)
		fmt.Fprintf(os.Stderr, "applied %d command(s); short-lived system-temp backup: %s\n", len(commands), bp)
	}
	if missed > 0 {
		return 1
	}
	return 0
}

func runRollback(target string, asJSON bool) int {
	abs, err := filepath.Abs(target)
	if err != nil {
		abs = target
	}
	bp := backupPath(abs)
	data, err := os.ReadFile(bp)
	if err != nil {
		msg := "backup not found; apply may not have run, clean may have removed it, or the system may have cleaned temp"
		if asJSON {
			printJSON(Result{Action: "rollback", Target: abs, OK: false, BackupPath: bp, Message: msg})
		} else {
			fmt.Fprintln(os.Stderr, msg)
		}
		return 1
	}

	// Warn before rollback overwrites the current file and discards changes made after apply.
	fmt.Fprintf(os.Stderr, "WARNING: restoring the backup over %s; changes made after apply will be lost.\n", abs)

	mode := os.FileMode(0644)
	if info, e := os.Stat(abs); e == nil {
		mode = info.Mode()
	}
	if err := writeAtomic(abs, data, mode); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write rollback data: %v\n", err)
		return 2
	}

	res := Result{Action: "rollback", Target: abs, OK: true, BackupPath: bp, Message: "restored from backup; the backup remains until the next apply to this file"}
	if asJSON {
		printJSON(res)
	} else {
		fmt.Fprintf(os.Stderr, "restored %s from backup\n", abs)
	}
	return 0
}

func runClean(asJSON bool) int {
	dir := filepath.Join(os.TempDir(), "clipfit")
	entries, _ := os.ReadDir(dir)
	n := len(entries)
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "failed to clean backups: %v\n", err)
		return 2
	}
	res := Result{Action: "clean", OK: true, Message: fmt.Sprintf("removed %d backup(s)", n)}
	if asJSON {
		printJSON(res)
	} else {
		fmt.Fprintf(os.Stderr, "removed %d backup(s) from %s\n", n, dir)
	}
	return 0
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage:")
	fmt.Fprintln(os.Stderr, "  clipfit apply    [--dry-run] [--json] <target-file> <spec-file>")
	fmt.Fprintln(os.Stderr, "  clipfit rollback [--json] <target-file>")
	fmt.Fprintln(os.Stderr, "  clipfit clean    [--json]")
	fmt.Fprintln(os.Stderr, "  clipfit mcp      [--root <directory>]")
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "apply":
		fs := flag.NewFlagSet("apply", flag.ExitOnError)
		dryRun := fs.Bool("dry-run", false, "preview changes without modifying the file or backup")
		asJSON := fs.Bool("json", false, "write the JSON result to stdout")
		fs.Parse(os.Args[2:])
		a := fs.Args()
		if len(a) != 2 {
			usage()
			os.Exit(2)
		}
		os.Exit(runApply(a[0], a[1], *dryRun, *asJSON))
	case "rollback":
		fs := flag.NewFlagSet("rollback", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "write the JSON result to stdout")
		fs.Parse(os.Args[2:])
		a := fs.Args()
		if len(a) != 1 {
			usage()
			os.Exit(2)
		}
		os.Exit(runRollback(a[0], *asJSON))
	case "clean":
		fs := flag.NewFlagSet("clean", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "write the JSON result to stdout")
		fs.Parse(os.Args[2:])
		os.Exit(runClean(*asJSON))
	case "mcp":
		fs := flag.NewFlagSet("mcp", flag.ExitOnError)
		root := fs.String("root", ".", "filesystem root exposed by MCP")
		fs.Parse(os.Args[2:])
		if fs.NArg() != 0 {
			usage()
			os.Exit(2)
		}
		os.Exit(runMCP(*root))
	default:
		usage()
		os.Exit(2)
	}
}
