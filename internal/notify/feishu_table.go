package notify

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxFeishuCardTables = 5

type parsedTable struct {
	headers []string
	rows    [][]string
}

type conclusionSegment struct {
	markdown string
	table    *parsedTable
}

// parseConclusionSegments splits normalized terminal conclusion text into alternating
// Markdown prose segments and structured tables (box-drawing, borderless TUI, and GFM pipe).
func parseConclusionSegments(s string) []conclusionSegment {
	lines := strings.Split(s, "\n")
	var segments []conclusionSegment
	var mdBuf []string

	flushMD := func() {
		if len(mdBuf) == 0 {
			return
		}
		text := strings.Trim(strings.Join(mdBuf, "\n"), "\n")
		mdBuf = nil
		if strings.TrimSpace(text) != "" {
			segments = append(segments, conclusionSegment{markdown: text})
		}
	}

	inFence := false
	var fenceChar byte
	fenceLen := 0

	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if fc, fl, ok := parseFenceLine(trimmed); ok {
			if !inFence {
				inFence = true
				fenceChar = fc
				fenceLen = fl
			} else if fc == fenceChar && fl >= fenceLen && isFenceClose(trimmed, fc) {
				inFence = false
			}
			mdBuf = append(mdBuf, line)
			i++
			continue
		}

		if !inFence {
			if tbl, rawLines, consumed, ok := tryParseTableAt(lines, i); ok {
				if tbl != nil {
					flushMD()
					segments = append(segments, conclusionSegment{table: tbl})
				} else {
					// Single-column box or header-only table: preserve monospace alignment in a code block.
					mdBuf = append(mdBuf, wrapInCodeFence(rawLines)...)
				}
				i += consumed
				continue
			}

			if strings.HasPrefix(trimmed, "• ") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				line = indent + "- " + strings.TrimPrefix(trimmed, "• ")
			}
		}

		mdBuf = append(mdBuf, line)
		i++
	}

	flushMD()
	return segments
}

func parseFenceLine(trimmed string) (byte, int, bool) {
	if len(trimmed) < 3 {
		return 0, 0, false
	}
	ch := trimmed[0]
	if ch != '`' && ch != '~' {
		return 0, 0, false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == ch {
		n++
	}
	if n < 3 {
		return 0, 0, false
	}
	return ch, n, true
}

func isFenceClose(trimmed string, ch byte) bool {
	for i := 0; i < len(trimmed); i++ {
		if trimmed[i] != ch && trimmed[i] != ' ' && trimmed[i] != '\t' {
			return false
		}
	}
	return true
}

func wrapInCodeFence(lines []string) []string {
	fence := "```"
	for _, l := range lines {
		if strings.Contains(l, "```") {
			fence = "````"
			break
		}
	}
	out := make([]string, 0, len(lines)+2)
	out = append(out, fence)
	out = append(out, lines...)
	out = append(out, fence)
	return out
}

func buildFeishuTableElement(tbl *parsedTable, index int) map[string]any {
	cols := make([]any, 0, len(tbl.headers))
	for i, h := range tbl.headers {
		title := strings.TrimSpace(h)
		if title == "" {
			title = fmt.Sprintf("列 %d", i+1)
		}
		cols = append(cols, map[string]any{
			"name":             fmt.Sprintf("c%d", i),
			"display_name":     title,
			"width":            "auto",
			"data_type":        "text",
			"vertical_align":   "top",
			"horizontal_align": "left",
		})
	}

	rows := make([]any, 0, len(tbl.rows))
	for _, r := range tbl.rows {
		rowObj := make(map[string]any, len(tbl.headers))
		for i := range tbl.headers {
			val := ""
			if i < len(r) {
				val = r[i]
			}
			rowObj[fmt.Sprintf("c%d", i)] = val
		}
		rows = append(rows, rowObj)
	}

	pageSize := len(rows)
	if pageSize < 1 {
		pageSize = 1
	} else if pageSize > 10 {
		pageSize = 10
	}

	return map[string]any{
		"tag":            "table",
		"element_id":     fmt.Sprintf("tbl_%d", index),
		"margin":         "0px 0px 8px 0px",
		"page_size":      pageSize,
		"row_height":     "auto",
		"row_max_height": "300px",
		"header_style": map[string]any{
			"text_align":       "left",
			"text_size":        "normal",
			"background_style": "grey",
			"text_color":       "default",
			"bold":             true,
			"lines":            2,
		},
		"columns": cols,
		"rows":    rows,
	}
}

func formatTableFallbackMarkdown(tbl *parsedTable) string {
	var lines []string
	lines = append(lines, "| "+strings.Join(tbl.headers, " | ")+" |")
	seps := make([]string, len(tbl.headers))
	for i := range seps {
		seps[i] = "---"
	}
	lines = append(lines, "| "+strings.Join(seps, " | ")+" |")
	for _, r := range tbl.rows {
		row := make([]string, len(tbl.headers))
		for i := range tbl.headers {
			if i < len(r) {
				row[i] = strings.ReplaceAll(r[i], "|", "\\|")
			}
		}
		lines = append(lines, "| "+strings.Join(row, " | ")+" |")
	}
	return strings.Join(wrapInCodeFence(lines), "\n")
}

func tryParseTableAt(lines []string, start int) (tbl *parsedTable, rawLines []string, consumed int, ok bool) {
	if tbl, rawLines, consumed, ok = tryParseMarkdownTable(lines, start); ok {
		return tbl, rawLines, consumed, true
	}
	if tbl, rawLines, consumed, ok = tryParseBoxTable(lines, start); ok {
		return tbl, rawLines, consumed, true
	}
	if tbl, rawLines, consumed, ok = tryParseBorderlessTable(lines, start); ok {
		return tbl, rawLines, consumed, true
	}
	return nil, nil, 0, false
}

// --- 1. Box-drawing tables (┌─┬─┐ / │...│ / ├─┼─┤ / └─┴─┘ / +---+) ---

func tryParseBoxTable(lines []string, start int) (*parsedTable, []string, int, bool) {
	trimmed := strings.TrimSpace(lines[start])
	if !isBoxTableStartLine(trimmed) {
		return nil, nil, 0, false
	}

	// Collect contiguous box-table lines.
	end := start
	for end < len(lines) {
		t := strings.TrimSpace(lines[end])
		if t == "" {
			break
		}
		if isBoxBorderLine(t) || isBoxDataLine(t) {
			end++
			if isBoxBottomBorder(t) {
				break
			}
			continue
		}
		break
	}
	if end-start < 2 {
		return nil, nil, 0, false
	}
	raw := lines[start:end]

	// Determine expected separator rune ('│', '║', '┃', or '|') and column count.
	sepRune := detectBoxSepRune(raw)
	if sepRune == 0 {
		return nil, raw, len(raw), true
	}

	var headerGroup [][]string
	var bodyGroups [][][]string
	var curGroup [][]string
	seenSep := false
	numCols := 0
	col0Width := 0

	for idx, l := range raw {
		t := strings.TrimSpace(l)
		if (isBoxTopBorder(t) || isBoxBorderLine(t)) && !seenSep && len(curGroup) == 0 {
			continue
		}
		if isBoxBottomBorder(t) || (idx == len(raw)-1 && isBoxBorderLine(t)) {
			continue
		}
		if isBoxBorderLine(t) {
			if !seenSep {
				headerGroup = curGroup
				curGroup = nil
				seenSep = true
			} else if len(curGroup) > 0 {
				bodyGroups = append(bodyGroups, curGroup)
				curGroup = nil
			}
			continue
		}
		cells, cw := splitOuterBoxDataLine(l, sepRune)
		if len(cells) > numCols {
			numCols = len(cells)
		}
		if cw > col0Width {
			col0Width = cw
		}
		curGroup = append(curGroup, cells)
	}
	if len(curGroup) > 0 {
		if !seenSep {
			// Table had no middle separator at all (clipped top+bottom or 1 block).
			bodyGroups = append(bodyGroups, curGroup)
		} else {
			bodyGroups = append(bodyGroups, curGroup)
		}
	}

	if numCols < 2 || numCols > 50 {
		return nil, raw, len(raw), true
	}

	tbl := assembleParsedTable(numCols, col0Width, headerGroup, bodyGroups, seenSep && len(bodyGroups) > 1)
	if len(tbl.rows) == 0 {
		return nil, raw, len(raw), true
	}
	return tbl, raw, len(raw), true
}

func isBoxTableStartLine(trimmed string) bool {
	if isBoxBorderLine(trimmed) {
		return true
	}
	if isBoxDataLine(trimmed) {
		// Require at least 2 columns (outer left + inner sep + outer right) so single-line
		// terminal bars aren't mistaken for box tables.
		sep := detectBoxSepRune([]string{trimmed})
		if sep != 0 {
			cells, _ := splitOuterBoxDataLine(trimmed, sep)
			return len(cells) >= 2
		}
	}
	return false
}

func isBoxDataLine(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	last, _ := utf8.DecodeLastRuneInString(trimmed)
	switch first {
	case '│', '║', '┃':
		return last == first || last == '│' || last == '║' || last == '┃'
	case '|':
		return last == '|' && len(trimmed) >= 3
	default:
		return false
	}
}

func isBoxBorderLine(trimmed string) bool {
	if len([]rune(trimmed)) < 3 {
		return false
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	switch first {
	case '┌', '╭', '╔', '┏', '├', '╠', '┣', '└', '╰', '╚', '┗':
		for _, r := range trimmed {
			switch r {
			case '─', '━', '═', '┌', '┬', '┐', '├', '┼', '┤', '└', '┴', '┘',
				'╭', '╮', '╰', '╯', '╔', '╦', '╗', '╠', '╬', '╣', '╚', '╩', '╝',
				'┏', '┳', '┓', '┣', '╋', '┫', '┗', '┻', '┛', '┯', '┷', '┿', '╂', ' ':
			default:
				return false
			}
		}
		return true
	case '+':
		hasDash := false
		for _, r := range trimmed {
			switch r {
			case '-', '=', '+':
				if r == '-' || r == '=' {
					hasDash = true
				}
			default:
				return false
			}
		}
		return hasDash
	default:
		return false
	}
}

func isBoxTopBorder(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	return first == '┌' || first == '╭' || first == '╔' || first == '┏'
}

func isBoxBottomBorder(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(trimmed)
	return first == '└' || first == '╰' || first == '╚' || first == '┗'
}

func detectBoxSepRune(lines []string) rune {
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if isBoxDataLine(t) {
			r, _ := utf8.DecodeRuneInString(t)
			return r
		}
	}
	return 0
}

func splitOuterBoxDataLine(line string, sep rune) ([]string, int) {
	t := strings.TrimSpace(line)
	sepStr := string(sep)
	t = strings.TrimPrefix(t, sepStr)
	t = strings.TrimSuffix(t, sepStr)
	parts := strings.Split(t, sepStr)
	col0Width := 0
	if len(parts) > 0 {
		col0Width = stringDisplayWidth(strings.TrimRight(parts[0], " "))
		// Strip single padding space at start of col 0 if present.
		if strings.HasPrefix(parts[0], " ") && col0Width > 0 {
			col0Width--
		}
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts, col0Width
}

// --- 2. Standard GFM Markdown pipe tables ---

func tryParseMarkdownTable(lines []string, start int) (*parsedTable, []string, int, bool) {
	if start+1 >= len(lines) {
		return nil, nil, 0, false
	}
	sepCols, ok := parseMarkdownSepRow(lines[start+1])
	if !ok || sepCols < 2 || sepCols > 50 {
		return nil, nil, 0, false
	}
	hdr := splitMarkdownPipeRow(lines[start])
	if len(hdr) < 2 {
		return nil, nil, 0, false
	}

	numCols := sepCols
	if len(hdr) > numCols {
		numCols = len(hdr)
	}

	end := start + 2
	var rows [][]string
	for end < len(lines) {
		t := strings.TrimSpace(lines[end])
		if t == "" || !strings.Contains(t, "|") {
			break
		}
		if _, _, isFence := parseFenceLine(t); isFence {
			break
		}
		row := splitMarkdownPipeRow(lines[end])
		if len(row) < 2 {
			break
		}
		rows = append(rows, padRow(row, numCols))
		end++
	}
	if len(rows) == 0 {
		return nil, nil, 0, false
	}
	return &parsedTable{
		headers: padRow(hdr, numCols),
		rows:    rows,
	}, lines[start:end], end - start, true
}

func parseMarkdownSepRow(line string) (int, bool) {
	t := strings.TrimSpace(line)
	if !strings.Contains(t, "|") || !strings.Contains(t, "-") {
		return 0, false
	}
	cells := splitMarkdownPipeRow(t)
	if len(cells) < 2 {
		return 0, false
	}
	for _, c := range cells {
		if !isMarkdownSepCell(c) {
			return 0, false
		}
	}
	return len(cells), true
}

func isMarkdownSepCell(cell string) bool {
	c := strings.TrimSpace(cell)
	if c == "" {
		return false
	}
	c = strings.TrimPrefix(c, ":")
	c = strings.TrimSuffix(c, ":")
	if c == "" {
		return false
	}
	for i := 0; i < len(c); i++ {
		if c[i] != '-' {
			return false
		}
	}
	return true
}

func splitMarkdownPipeRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	if len(t) > 0 && t[len(t)-1] == '|' && (len(t) < 2 || t[len(t)-2] != '\\') {
		t = t[:len(t)-1]
	}
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(t); i++ {
		if t[i] == '\\' && i+1 < len(t) && t[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if t[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(t[i])
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

// --- 3. Borderless-outer-edge TUI tables (Antigravity CLI, Rich, Pi, etc.) ---

func tryParseBorderlessTable(lines []string, start int) (*parsedTable, []string, int, bool) {
	sepChar, basePos := detectSepPositions(lines[start])
	if sepChar == 0 || len(basePos) == 0 || len(basePos)+1 > 50 {
		return nil, nil, 0, false
	}

	// Look ahead up to 3 lines for an explicit borderless separator line (e.g. ────┼──── or ---+---).
	sepIdx := -1
	lookLimit := start + 4
	if lookLimit > len(lines) {
		lookLimit = len(lines)
	}
	for k := start + 1; k < lookLimit; k++ {
		if isBorderlessSepLine(lines[k]) {
			sepIdx = k
			break
		}
		sc, pos := detectSepPositions(lines[k])
		if sc != sepChar || !positionsAligned(basePos, pos) {
			break
		}
	}

	var end int
	hasExplicitSep := sepIdx != -1
	if hasExplicitSep {
		end = sepIdx + 1
	} else {
		end = start + 1
	}

	dataLines := 1
	hasPadding := lineHasTablePadding(lines[start], sepChar)
	for end < len(lines) {
		l := lines[end]
		if strings.TrimSpace(l) == "" {
			// Allow a blank row spacer inside the table only if the very next line continues the table.
			if end+1 < len(lines) {
				next := lines[end+1]
				sc, pos := detectSepPositions(next)
				if (sc == sepChar && positionsAligned(basePos, pos)) || isBorderlessSepLine(next) {
					end++
					continue
				}
			}
			break
		}
		if isBorderlessSepLine(l) {
			end++
			continue
		}
		sc, pos := detectSepPositions(l)
		if sc == sepChar && positionsAligned(basePos, pos) {
			dataLines++
			if lineHasTablePadding(l, sepChar) {
				hasPadding = true
			}
			end++
			continue
		}
		break
	}

	if !hasExplicitSep {
		if dataLines < 2 {
			return nil, nil, 0, false
		}
		// Guard against two short coincidental ASCII pipe lines like "foo | bar\nbaz | qux".
		if sepChar == '|' && len(basePos) == 1 && dataLines < 3 && !hasPadding {
			return nil, nil, 0, false
		}
	} else if end <= sepIdx+1 {
		// Header + separator with zero body rows: not a table we can render cleanly as Card 2.0 table.
		return nil, nil, 0, false
	}

	raw := lines[start:end]
	numCols := len(basePos) + 1
	col0Width := basePos[0]
	if col0Width > 1 {
		col0Width-- // Typical 1-space padding before column separator
	}

	var headerGroup [][]string
	var bodyGroups [][][]string
	var curGroup [][]string
	seenHeaderSep := false
	hasBodyRowSep := false

	for idx, l := range raw {
		if isBorderlessSepLine(l) {
			if hasExplicitSep && start+idx == sepIdx {
				headerGroup = curGroup
				curGroup = nil
				seenHeaderSep = true
			} else if len(curGroup) > 0 {
				bodyGroups = append(bodyGroups, curGroup)
				curGroup = nil
				hasBodyRowSep = true
			}
			continue
		}
		if strings.TrimSpace(l) == "" {
			if len(curGroup) > 0 {
				bodyGroups = append(bodyGroups, curGroup)
				curGroup = nil
				hasBodyRowSep = true
			}
			continue
		}
		cells := splitBorderlessLine(l, sepChar, basePos)
		curGroup = append(curGroup, cells)
	}
	if len(curGroup) > 0 {
		if hasExplicitSep && !seenHeaderSep {
			return nil, nil, 0, false
		}
		bodyGroups = append(bodyGroups, curGroup)
	}

	tbl := assembleParsedTable(numCols, col0Width, headerGroup, bodyGroups, hasBodyRowSep)
	if len(tbl.rows) == 0 {
		return nil, nil, 0, false
	}
	return tbl, raw, len(raw), true
}

func detectSepPositions(line string) (rune, []int) {
	if strings.TrimSpace(line) == "" || isBorderlessSepLine(line) {
		return 0, nil
	}
	// Prefer Unicode box bar '│', otherwise ASCII '|'.
	var boxPos []int
	var pipePos []int
	width := 0
	for _, r := range line {
		if r == '│' {
			boxPos = append(boxPos, width)
		} else if r == '|' {
			pipePos = append(pipePos, width)
		}
		width += runeDisplayWidth(r)
	}
	if len(boxPos) > 0 {
		return '│', boxPos
	}
	if len(pipePos) > 0 {
		return '|', pipePos
	}
	return 0, nil
}

func positionsAligned(a, b []int) bool {
	if len(a) != len(b) || len(a) == 0 {
		return false
	}
	for i := range a {
		diff := a[i] - b[i]
		if diff < 0 {
			diff = -diff
		}
		if diff > 2 {
			return false
		}
	}
	return true
}

func lineHasTablePadding(line string, sepChar rune) bool {
	sep := string(sepChar)
	return strings.Contains(line, "  "+sep) || strings.HasPrefix(line, "  ")
}

func isBorderlessSepLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if len([]rune(trimmed)) < 5 {
		return false
	}
	dashes := 0
	junctions := 0
	segments := 0
	inDashRun := false
	runLen := 0

	for _, r := range trimmed {
		switch r {
		case '─', '━', '═', '-':
			dashes++
			runLen++
			if !inDashRun {
				inDashRun = true
			}
		case '┼', '╂', '╋', '╪', '╫', '╬', '┬', '┴', '┯', '┷', '+', '|', '│':
			junctions++
			if inDashRun && runLen >= 2 {
				segments++
			}
			inDashRun = false
			runLen = 0
		case ' ':
			if inDashRun && runLen >= 2 {
				segments++
			}
			inDashRun = false
			runLen = 0
		default:
			return false
		}
	}
	if inDashRun && runLen >= 2 {
		segments++
	}
	if dashes < 3 {
		return false
	}
	return junctions >= 1 || segments >= 2
}

func splitBorderlessLine(line string, sepChar rune, basePos []int) []string {
	numCols := len(basePos) + 1
	// Collect all occurrences of sepChar and their display positions.
	type sepHit struct {
		byteIdx int
		dispCol int
	}
	var hits []sepHit
	width := 0
	for idx, r := range line {
		if r == sepChar {
			hits = append(hits, sepHit{byteIdx: idx, dispCol: width})
		}
		width += runeDisplayWidth(r)
	}

	if len(hits) == len(basePos) {
		return splitAtByteIndices(line, func() []int {
			out := make([]int, len(hits))
			for i, h := range hits {
				out[i] = h.byteIdx
			}
			return out
		}(), utf8.RuneLen(sepChar))
	}

	// If a cell contains an extra literal separator, pick the hits closest to basePos.
	chosen := make([]int, 0, len(basePos))
	hitIdx := 0
	for _, target := range basePos {
		best := -1
		bestDiff := 1 << 30
		for j := hitIdx; j < len(hits); j++ {
			diff := hits[j].dispCol - target
			if diff < 0 {
				diff = -diff
			}
			if diff < bestDiff {
				bestDiff = diff
				best = j
			}
		}
		if best >= 0 {
			chosen = append(chosen, hits[best].byteIdx)
			hitIdx = best + 1
		}
	}
	out := splitAtByteIndices(line, chosen, utf8.RuneLen(sepChar))
	return padRow(out, numCols)
}

func splitAtByteIndices(line string, indices []int, sepLen int) []string {
	out := make([]string, 0, len(indices)+1)
	prev := 0
	for _, idx := range indices {
		out = append(out, strings.TrimSpace(line[prev:idx]))
		prev = idx + sepLen
	}
	out = append(out, strings.TrimSpace(line[prev:]))
	return out
}

// --- Shared table row assembly & cell unwrapping ---

func assembleParsedTable(numCols, col0Width int, headerGroup [][]string, bodyGroups [][][]string, explicitRowSplit bool) *parsedTable {
	var headers []string
	if len(headerGroup) > 0 {
		headers = mergeCellLines(headerGroup, numCols)
	} else {
		headers = make([]string, numCols)
		for i := range headers {
			headers[i] = fmt.Sprintf("列 %d", i+1)
		}
	}

	var rows [][]string
	if explicitRowSplit {
		for _, g := range bodyGroups {
			if len(g) == 0 {
				continue
			}
			rows = append(rows, mergeCellLines(g, numCols))
		}
	} else {
		for _, g := range bodyGroups {
			rows = append(rows, splitAndMergeUnseparatedRows(g, numCols, col0Width)...)
		}
	}

	return &parsedTable{
		headers: padRow(headers, numCols),
		rows:    rows,
	}
}

func splitAndMergeUnseparatedRows(lines [][]string, numCols, col0Width int) [][]string {
	var rows [][]string
	var curGroup [][]string
	var prevLine []string

	flush := func() {
		if len(curGroup) > 0 {
			rows = append(rows, mergeCellLines(curGroup, numCols))
			curGroup = nil
		}
	}

	for _, rawCells := range lines {
		cells := padRow(rawCells, numCols)
		if allCellsEmpty(cells) {
			flush()
			prevLine = nil
			continue
		}
		if len(curGroup) == 0 {
			curGroup = append(curGroup, cells)
			prevLine = cells
			continue
		}
		if isContinuationRowLine(prevLine, cells, col0Width) {
			curGroup = append(curGroup, cells)
		} else {
			flush()
			curGroup = append(curGroup, cells)
		}
		prevLine = cells
	}
	flush()
	return rows
}

func isContinuationRowLine(prev, next []string, col0Width int) bool {
	// Empty col 0 on the next line while a later column has content is an unambiguous continuation.
	if next[0] == "" {
		return true
	}
	if prev[0] == "" {
		return false
	}

	// If any cell on prev ends mid-token (e.g. "274-", "src/", "("), this is a wrapped row.
	hasWrappedTail := false
	for _, c := range prev {
		if endsWithWrapBreak(c) {
			hasWrappedTail = true
			break
		}
	}

	prev0W := stringDisplayWidth(prev[0])
	firstTokW := firstTokenDisplayWidth(next[0])
	if col0Width > 0 && prev0W >= 12 && prev0W+1+firstTokW > col0Width && (hasWrappedTail || prev0W*10 >= col0Width*8) {
		return true
	}
	return false
}

func endsWithWrapBreak(cell string) bool {
	if cell == "" {
		return false
	}
	r, size := utf8.DecodeLastRuneInString(cell)
	switch r {
	case '/', '(', '[', '（', '【', '“':
		return true
	case '-':
		if len(cell) > size {
			prev, _ := utf8.DecodeLastRuneInString(cell[:len(cell)-size])
			return prev != ' '
		}
	}
	return false
}

func allCellsEmpty(cells []string) bool {
	for _, c := range cells {
		if c != "" {
			return false
		}
	}
	return true
}

func mergeCellLines(group [][]string, numCols int) []string {
	perCol := make([][]string, numCols)
	for _, line := range group {
		for c := 0; c < numCols && c < len(line); c++ {
			if t := strings.TrimSpace(line[c]); t != "" {
				perCol[c] = append(perCol[c], t)
			}
		}
	}
	out := make([]string, numCols)
	for c := 0; c < numCols; c++ {
		out[c] = joinWrappedCellLines(perCol[c])
	}
	return out
}

func joinWrappedCellLines(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, next := range parts[1:] {
		if next == "" {
			continue
		}
		if shouldJoinWithoutSpace(out, next) {
			out += next
		} else {
			out += " " + next
		}
	}
	return out
}

func shouldJoinWithoutSpace(prev, next string) bool {
	last, lastSize := utf8.DecodeLastRuneInString(prev)
	first, _ := utf8.DecodeRuneInString(next)

	switch last {
	case '/', '(', '[', '“', '（', '【', '「', '《':
		return true
	case '-':
		if len(prev) > lastSize {
			before, _ := utf8.DecodeLastRuneInString(prev[:len(prev)-lastSize])
			if before != ' ' {
				return true
			}
		}
	}
	switch first {
	case ')', ']', ',', '.', ':', ';', '”', '）', '】', '」', '》', '，', '。', '：', '；', '、':
		return true
	}
	if isWideRune(last) && isWideRune(first) {
		return true
	}
	if isCJKBubblePunct(last) || isCJKBubblePunct(first) {
		return true
	}
	return false
}

func isCJKBubblePunct(r rune) bool {
	switch r {
	case '，', '。', '：', '；', '、', '（', '）', '【', '】', '《', '》', '「', '」':
		return true
	}
	return false
}

func padRow(row []string, n int) []string {
	if len(row) == n {
		return row
	}
	out := make([]string, n)
	copy(out, row)
	return out
}

func firstTokenDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		if r == ' ' || r == '\t' {
			break
		}
		rw := runeDisplayWidth(r)
		if isWideRune(r) {
			if w == 0 {
				return rw
			}
			break
		}
		w += rw
	}
	return w
}

func stringDisplayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

func runeDisplayWidth(r rune) int {
	if r < 0x20 || unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	if isWideRune(r) {
		return 2
	}
	return 1
}

func isWideRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) ||
		(r >= 0x2E80 && r <= 0x9FFF) ||
		(r >= 0xA960 && r <= 0xA97F) ||
		(r >= 0xAC00 && r <= 0xD7AF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE10 && r <= 0xFE1F) ||
		(r >= 0xFE30 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x1F300 && r <= 0x1FAFF) ||
		(r >= 0x20000 && r <= 0x3FFFD)
}
