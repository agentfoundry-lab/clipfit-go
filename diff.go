package main

// lineEditKind is an internal representation used to turn a shortest edit
// script into the compact Hunk format returned by ClipFit.
type lineEditKind uint8

const (
	lineEqual lineEditKind = iota
	lineDelete
	lineInsert
)

type lineEdit struct {
	Kind lineEditKind
	Text string
}

// Keep worst-case Myers trace memory bounded. On 64-bit Go this is roughly
// 32 MiB of integer storage, excluding the small slice headers.
const maxMyersTraceInts = 4_000_000

// computeLocalizedChanges uses a line-based Myers diff so distant edits remain
// separate hunks even when an earlier edit changes the total line count.
func computeChanges(before, after string) []Hunk {
	if before == after {
		return nil
	}
	beforeLines := splitDiffLines(before)
	afterLines := splitDiffLines(after)

	prefix := 0
	for prefix < len(beforeLines) && prefix < len(afterLines) && beforeLines[prefix] == afterLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(beforeLines)-prefix &&
		suffix < len(afterLines)-prefix &&
		beforeLines[len(beforeLines)-1-suffix] == afterLines[len(afterLines)-1-suffix] {
		suffix++
	}

	beforeMiddle := beforeLines[prefix : len(beforeLines)-suffix]
	afterMiddle := afterLines[prefix : len(afterLines)-suffix]
	if len(beforeMiddle) == 0 || len(afterMiddle) == 0 {
		return []Hunk{newHunk(prefix+1, beforeMiddle, afterMiddle)}
	}

	edits, ok := myersLineDiff(beforeMiddle, afterMiddle)
	if !ok {
		// This preserves correctness while the MCP response budget prevents an
		// unexpectedly huge fallback hunk from issuing an apply-capable preview.
		return []Hunk{newHunk(prefix+1, beforeMiddle, afterMiddle)}
	}
	return lineEditsToHunks(edits, prefix+1)
}

func splitDiffLines(text string) []string {
	// Keep the final empty element so adding/removing a trailing newline remains
	// observable in the diff.
	lines := make([]string, 1)
	start := 0
	for index := 0; index < len(text); index++ {
		if text[index] == '\n' {
			lines[len(lines)-1] = text[start:index]
			lines = append(lines, "")
			start = index + 1
		}
	}
	lines[len(lines)-1] = text[start:]
	return lines
}

func newHunk(oldStart int, removed, added []string) Hunk {
	removedCopy := append([]string{}, removed...)
	addedCopy := append([]string{}, added...)
	return Hunk{OldStart: oldStart, Removed: removedCopy, Added: addedCopy}
}

func myersLineDiff(before, after []string) ([]lineEdit, bool) {
	beforeCount, afterCount := len(before), len(after)
	maxDistance := beforeCount + afterCount
	width := 2*maxDistance + 3
	if width > maxMyersTraceInts {
		return nil, false
	}
	offset := maxDistance + 1
	frontier := make([]int, width)
	frontier[offset+1] = 0
	traceCapacity := maxMyersTraceInts / width
	if traceCapacity > maxDistance+1 {
		traceCapacity = maxDistance + 1
	}
	trace := make([][]int, 0, traceCapacity)

	for distance := 0; distance <= maxDistance; distance++ {
		if (len(trace)+1)*width > maxMyersTraceInts {
			return nil, false
		}
		for diagonal := -distance; diagonal <= distance; diagonal += 2 {
			index := offset + diagonal
			var x int
			if diagonal == -distance ||
				(diagonal != distance && frontier[index-1] < frontier[index+1]) {
				x = frontier[index+1]
			} else {
				x = frontier[index-1] + 1
			}
			y := x - diagonal
			for x < beforeCount && y < afterCount && before[x] == after[y] {
				x++
				y++
			}
			frontier[index] = x
			if x >= beforeCount && y >= afterCount {
				trace = append(trace, append([]int(nil), frontier...))
				return backtrackMyersLines(trace, before, after, offset), true
			}
		}
		trace = append(trace, append([]int(nil), frontier...))
	}
	return nil, false
}

func backtrackMyersLines(trace [][]int, before, after []string, offset int) []lineEdit {
	x, y := len(before), len(after)
	reversed := make([]lineEdit, 0, x+y)

	for distance := len(trace) - 1; distance > 0; distance-- {
		previous := trace[distance-1]
		diagonal := x - y
		var previousDiagonal int
		if diagonal == -distance ||
			(diagonal != distance && previous[offset+diagonal-1] < previous[offset+diagonal+1]) {
			previousDiagonal = diagonal + 1
		} else {
			previousDiagonal = diagonal - 1
		}
		previousX := previous[offset+previousDiagonal]
		previousY := previousX - previousDiagonal

		for x > previousX && y > previousY {
			reversed = append(reversed, lineEdit{Kind: lineEqual, Text: before[x-1]})
			x--
			y--
		}
		if x == previousX {
			reversed = append(reversed, lineEdit{Kind: lineInsert, Text: after[y-1]})
			y--
		} else {
			reversed = append(reversed, lineEdit{Kind: lineDelete, Text: before[x-1]})
			x--
		}
	}

	for x > 0 && y > 0 {
		reversed = append(reversed, lineEdit{Kind: lineEqual, Text: before[x-1]})
		x--
		y--
	}
	for x > 0 {
		reversed = append(reversed, lineEdit{Kind: lineDelete, Text: before[x-1]})
		x--
	}
	for y > 0 {
		reversed = append(reversed, lineEdit{Kind: lineInsert, Text: after[y-1]})
		y--
	}

	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	return reversed
}

func lineEditsToHunks(edits []lineEdit, oldStart int) []Hunk {
	oldLine := oldStart
	var hunks []Hunk
	var current *Hunk
	flush := func() {
		if current != nil {
			hunks = append(hunks, *current)
			current = nil
		}
	}
	startHunk := func() {
		if current == nil {
			hunk := newHunk(oldLine, nil, nil)
			current = &hunk
		}
	}

	for _, edit := range edits {
		switch edit.Kind {
		case lineEqual:
			flush()
			oldLine++
		case lineDelete:
			startHunk()
			current.Removed = append(current.Removed, edit.Text)
			oldLine++
		case lineInsert:
			startHunk()
			current.Added = append(current.Added, edit.Text)
		}
	}
	flush()
	return hunks
}
