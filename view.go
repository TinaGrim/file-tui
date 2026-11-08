package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const minDetail = 30

func (m Model) View() string {
	if m.quitting || !m.ready {
		return ""
	}
	if m.width < 30 || m.height < 10 {
		return "\n  " + accent("file") + "\n  " + dim("terminal too small") + "\n"
	}
	switch m.screen {
	case scrHelp:
		return m.viewHelp()
	case scrProgress:
		return m.viewProgress()
	case scrSummary:
		return m.viewSummary()
	default:
		return m.viewBrowse()
	}
}

// ------------------------------------------------------------------- shared

func (m Model) rule() string {
	return styFaint.Render(strings.Repeat("─", max(1, m.width)))
}

func (m Model) pageHeader(section, right string) string {
	left := " " + accent("file") + "  " + styFaint.Render("·") + "  " + styStrong.Render(section)
	if right == "" {
		return left
	}
	return alignRight(left, styDim.Render(right), m.width)
}

func alignRight(left, right string, width int) string {
	if right == "" {
		return left
	}
	gap := width - visibleLen(left) - visibleLen(right) - 1
	if gap < 1 {
		return left
	}
	return left + strings.Repeat(" ", gap) + right + " "
}

func (m Model) fit(lines []string) string {
	if len(lines) > m.height {
		if m.height <= 2 {
			lines = lines[:m.height]
		} else {
			tail := append([]string{}, lines[len(lines)-2:]...)
			lines = append(lines[:m.height-2], tail...)
		}
	}
	return strings.Join(lines, "\n")
}

// ------------------------------------------------------------------- browse

func (m Model) twoPane() bool { return m.width >= 88 }

func (m Model) detailWidth() int {
	if !m.twoPane() {
		return 0
	}
	return clampInt(m.width*31/100, minDetail, 42)
}

func (m Model) listWidth() int {
	if !m.twoPane() {
		return max(20, m.width)
	}
	return max(30, m.width-m.detailWidth()-3)
}

func (m Model) viewBrowse() string {
	lines := make([]string, 0, m.height)
	lines = append(lines, m.browseHeader(), m.rule(), m.columnHeader())

	rows := m.renderList(m.listWidth())
	if m.twoPane() {
		rows = joinPane(rows, m.listWidth(), m.renderDetail(m.detailWidth()), m.detailWidth())
	}
	lines = append(lines, rows...)

	lines = append(lines, m.rule())
	if m.overlay != ovNone {
		lines = append(lines, m.overlayLines()...)
	} else {
		lines = append(lines, m.statusLine(), m.footerLine())
	}
	return m.fit(lines)
}

func (m Model) browseHeader() string {
	right := ""
	if m.width >= 68 {
		right = m.folderSummary()
	}
	brand := " " + accent("file")
	room := m.width - visibleLen(brand) - 4
	if right != "" {
		room -= visibleLen(right) + 2
	}
	path := elidePath(shortPath(m.dir), max(8, room))
	left := brand + "  " + styDim.Render(path)
	if right == "" {
		return left
	}
	return alignRight(left, styDim.Render(right), m.width)
}

func (m Model) columnHeader() string {
	lw := m.listWidth()
	left := m.listHeader(lw)
	if !m.twoPane() {
		return pad(left, m.width)
	}
	dw := m.detailWidth()
	right := styDim.Render("DETAILS")
	return pad(left, lw) + " " + styFaint.Render("│") + " " + pad(right, dw)
}

func sortGlyph(mode, active sortMode, reverse bool) string {
	if mode != active {
		return ""
	}
	desc := mode.descendingByDefault()
	if reverse {
		desc = !desc
	}
	if desc {
		return " ↓"
	}
	return " ↑"
}

type listCols struct {
	nameW int
	sizeW int
	modW  int
	size  bool
	mod   bool
}

func layoutFor(w int) listCols {
	c := listCols{nameW: w, size: w >= 38, mod: w >= 58}
	if c.size {
		c.sizeW = 10
	}
	if c.mod {
		c.modW = 12
	}
	gaps := 0
	if c.size {
		gaps += 2
	}
	if c.mod {
		gaps += 2
	}
	c.nameW = w - 5 - gaps - c.sizeW - c.modW
	if c.nameW < 10 && c.mod {
		c.mod, c.modW = false, 0
		c.nameW += 14
	}
	if c.nameW < 10 && c.size {
		c.size, c.sizeW = false, 0
		c.nameW += 12
	}
	return c
}

func (m Model) listHeader(w int) string {
	c := layoutFor(w)
	line := "     "

	name := "NAME" + sortGlyph(sortName, m.sort, m.reverse)
	if m.sort == sortName {
		line += pad(accent(name), c.nameW)
	} else {
		line += pad(styDim.Render(name), c.nameW)
	}

	if c.size {
		label := "SIZE" + sortGlyph(sortSize, m.sort, m.reverse)
		if m.sort == sortSize {
			line += "  " + padLeft(accent(label), c.sizeW)
		} else {
			line += "  " + padLeft(styDim.Render(label), c.sizeW)
		}
	}
	if c.mod {
		label := "MODIFIED" + sortGlyph(sortDate, m.sort, m.reverse)
		if m.sort == sortDate {
			line += "  " + pad(accent(label), c.modW)
		} else {
			line += "  " + pad(styDim.Render(label), c.modW)
		}
	}
	return pad(line, w)
}

func (m Model) renderList(w int) []string {
	h := m.listHeight()
	rows := make([]string, h)
	for i := range rows {
		rows[i] = ""
	}

	if len(m.visible) == 0 {
		switch {
		case m.loadErr != nil:
			rows[0] = " " + styBad.Render("✗ "+clip(m.loadErr.Error(), max(4, w-4)))
		case m.loading:
			rows[0] = " " + styDim.Render(spinner(m.spin)+" reading folder…")
		case m.filter != "":
			rows[0] = " " + styDim.Render("no matches · esc clears the filter")
		default:
			rows[0] = " " + styDim.Render("no visible items · H shows hidden files")
		}
		return rows
	}

	c := layoutFor(w)
	end := min(m.top+h, len(m.visible))
	row := 0
	for i := m.top; i < end && row < h; i++ {
		e := m.entries[m.visible[i]]
		rows[row] = m.renderRow(e, i == m.cursor, w, c)
		row++
	}
	return rows
}

func (m Model) renderRow(e Entry, current bool, w int, c listCols) string {
	bar := " "
	if current {
		bar = accent("▌")
	}
	dot := " "
	if m.sel[e.Path] {
		dot = accent("●")
	}
	gutter := " " + bar + " " + dot + " "

	name := e.Name
	if e.Dir && !e.IsParent() {
		name += "/"
	}
	if e.Link {
		name += "@"
	}
	name = clip(name, c.nameW)

	var styledName string
	switch {
	case current && e.Dir:
		styledName = accent(name)
	case current:
		styledName = styStrong.Render(name)
	case e.IsParent():
		styledName = styDim.Render(name)
	case e.Dir:
		styledName = accent(name)
	case e.Hidden:
		styledName = styDim.Render(name)
	case e.Link:
		styledName = styWarn.Render(name)
	default:
		styledName = name
	}

	line := gutter + pad(styledName, c.nameW)

	if c.size {
		size := ""
		if !e.IsParent() {
			if e.Dir {
				size = m.dirSizeCell(e.Path)
			} else {
				size = humanSize(e.Size)
			}
		}
		line += "  " + padLeft(styDim.Render(size), c.sizeW)
	}
	if c.mod {
		mod := ""
		if !e.IsParent() {
			mod = modLabel(e.ModAt)
		}
		line += "  " + styDim.Render(pad(mod, c.modW))
	}
	return pad(line, w)
}

func (m Model) dirSizeCell(path string) string {
	c, ok := m.sizes[path]
	if !ok || !c.info.done {
		return "…"
	}
	if c.info.err != "" {
		return "—"
	}
	size := humanSize(c.info.bytes)
	if c.info.errs > 0 {
		size = "~" + size
	}
	return size
}

func joinPane(list []string, lw int, detail []string, dw int) []string {
	out := make([]string, len(list))
	for i := range list {
		d := ""
		if i < len(detail) {
			d = detail[i]
		}
		out[i] = pad(list[i], lw) + " " + styFaint.Render("│") + " " + pad(d, dw)
	}
	return out
}

// ------------------------------------------------------------------- detail

func (m Model) renderDetail(w int) []string {
	h := m.listHeight()
	lines := []string{}
	e, ok := m.cur()
	if !ok {
		lines = append(lines, " "+styDim.Render("nothing selected"))
		return padLines(lines, h)
	}

	labelW := 10
	valueW := max(8, w-labelW-1)
	blank := strings.Repeat(" ", labelW+1)

	add := func(label, value string) {
		parts := wrapHard(value, valueW)
		for i, p := range parts {
			prefix := blank
			if i == 0 {
				prefix = pad(styDim.Render(label), labelW) + " "
			}
			lines = append(lines, prefix+p)
		}
	}
	addBold := func(label, value string) {
		parts := wrapHard(value, valueW)
		for i, p := range parts {
			prefix := blank
			if i == 0 {
				prefix = pad(styDim.Render(label), labelW) + " "
			}
			lines = append(lines, prefix+styStrong.Render(p))
		}
	}
	addWarn := func(label, value string) {
		parts := wrapHard(value, valueW)
		for i, p := range parts {
			prefix := blank
			if i == 0 {
				prefix = pad(styDim.Render(label), labelW) + " "
			}
			lines = append(lines, prefix+styWarn.Render(p))
		}
	}
	addElided := func(label, value string) {
		lines = append(lines, pad(styDim.Render(label), labelW)+" "+
			elidePath(value, valueW))
	}

	if e.IsParent() {
		addBold("name", "..")
		add("type", "parent folder")
		addElided("path", shortPath(e.Path))
		lines = append(lines, "", " "+styDim.Render("enter opens the folder above"))
		return padLines(lines, h)
	}

	addBold("name", e.Name)
	add("type", typeSubtitle(e))

	if e.Dir {
		if c, ok := m.sizes[e.Path]; ok && c.info.done {
			if c.info.err != "" {
				addWarn("size", "unavailable")
			} else {
				addBold("size", humanSize(c.info.bytes))
				add("contents", countNoun(c.info.files, "file", "files")+" · "+countNoun(c.info.dirs, "folder", "folders"))
				add("exact", exactBytes(c.info.bytes))
				if c.info.errs > 0 {
					addWarn("note", countNoun(c.info.errs, "entry was", "entries were")+" not readable")
				}
			}
		} else {
			add("size", spinner(m.spin)+" measuring…")
		}
	} else {
		addBold("size", humanSize(e.Size))
		add("exact", exactBytes(e.Size))
	}

	add("modified", modDetail(e.ModAt))
	add("permissions", permString(e.Mode))
	addElided("path", shortPath(e.Path))
	if e.Link && e.LinkTarget != "" {
		add("link to", e.LinkTarget)
	}

	if m.sel[e.Path] {
		lines = append(lines, "", " "+accent("● in selection"))
	}

	count, bytes, pending, unknown := m.selectedTotal()
	if count > 0 {
		lines = append(lines, "", " "+styDim.Render("SELECTION"))
		summary := countNoun(count, "item", "items") + " · " + humanSize(bytes)
		switch {
		case pending:
			if bytes > 0 {
				summary += "+"
			} else {
				summary = countNoun(count, "item", "items") + " · measuring…"
			}
		case unknown:
			if bytes > 0 {
				summary += "+"
			} else {
				summary = countNoun(count, "item", "items") + " · size unavailable"
			}
		}
		lines = append(lines, " "+summary)
	}

	return padLines(lines, h)
}

func padLines(lines []string, h int) []string {
	if len(lines) > h {
		return lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
}

// ------------------------------------------------------------------- status

func (m Model) statusLeft() (string, int) {
	if m.status != "" {
		if m.statusErr {
			return m.status, 2
		}
		return m.status, 1
	}
	if m.loadErr != nil {
		return m.loadErr.Error(), 2
	}
	return m.statusLineText(), 0
}

func (m Model) statusLine() string {
	if m.editing == 1 {
		return " " + accent("filter") + "  " + m.filterInput.View()
	}

	plain, kind := m.statusLeft()
	right := m.selectionSummary()
	if right != "" {
		right = styDim.Render(right)
	}

	avail := m.width - 2
	if right != "" {
		avail -= visibleLen(right) + 1
	}
	iconW := 0
	if kind != 0 {
		iconW = 3
	}
	if visibleLen(plain) > avail-iconW {
		plain = clip(plain, max(1, avail-iconW))
	}

	var left string
	switch kind {
	case 1:
		left = " " + accent("· ") + plain
	case 2:
		left = " " + styBad.Render("✗ ") + plain
	default:
		left = " " + styDim.Render(plain)
	}
	return alignRight(left, right, m.width)
}

func (m Model) footerLine() string {
	if m.editing == 1 {
		return keyHints([][2]string{{"enter", "apply"}, {"esc", "clear"}, {"ctrl+u", "clear text"}}, m.width)
	}
	var hints [][2]string
	switch {
	case m.width >= 90:
		hints = [][2]string{
			{"space", "select"}, {"enter", "open"}, {"d", "delete"}, {"z", "zip"},
			{"/", "filter"}, {"?", "help"}, {"q", "quit"}, {"s", "sort"}, {"H", "hidden"},
		}
	case m.width >= 64:
		hints = [][2]string{
			{"space", "select"}, {"d", "delete"}, {"z", "zip"},
			{"/", "filter"}, {"?", "help"}, {"q", "quit"}, {"enter", "open"},
		}
	case m.width >= 52:
		hints = [][2]string{
			{"space", "select"}, {"d", "delete"}, {"z", "zip"}, {"?", "help"}, {"q", "quit"},
		}
	default:
		hints = [][2]string{{"d", "delete"}, {"z", "zip"}, {"?", "help"}}
	}
	return keyHints(hints, m.width)
}

// ------------------------------------------------------------------ overlay

func (m Model) overlayLines() []string {
	switch m.overlay {
	case ovDelete:
		return m.deleteOverlay()
	case ovZipPrompt:
		return m.zipOverlay()
	}
	return nil
}

func (m Model) deleteOverlay() []string {
	count := len(m.deleteTargets)
	size := m.deleteModalSize()
	line1 := " " + styBad.Render("▌") + " " + styBad.Render("Delete") +
		"  " + bold(countNoun(count, "item", "items"))
	if m.width >= 48 {
		line1 += "  " + styDim.Render("· "+size)
	}

	names := make([]string, 0, len(m.deleteTargets))
	for _, p := range m.deleteTargets {
		names = append(names, filepath.Base(p))
	}
	nameLine := "   " + styDim.Render(clip(strings.Join(names, ", "), max(8, m.width-4)))

	if m.width < 46 {
		text := "t trash · D delete permanently · esc cancel"
		if !m.trashOK {
			text = "D delete permanently · esc cancel"
		}
		if m.width < 40 {
			text = "t trash · D delete · esc"
			if !m.trashOK {
				text = "D delete · esc"
			}
		}
		return []string{line1, nameLine, "   " + styDim.Render(clip(text, max(8, m.width-4)))}
	}

	var hints [][2]string
	if m.trashOK {
		if m.width >= 60 {
			hints = [][2]string{{"t", "trash"}, {"D", "delete permanently"}, {"esc", "cancel"}}
		} else {
			hints = [][2]string{{"t", "trash"}, {"D", "delete"}, {"esc", "cancel"}}
		}
	} else {
		hints = [][2]string{{"D", "delete permanently"}, {"esc", "cancel"}}
	}
	return []string{line1, nameLine, "  " + keyHints(hints, max(1, m.width-2))}
}

func (m Model) zipOverlay() []string {
	line1 := " " + accent("▌") + " " + styStrong.Render("New archive")
	if m.width >= 54 {
		line1 = alignRight(line1, styDim.Render("enter create  ·  esc cancel"), m.width)
	} else {
		line1 += "  " + styDim.Render("enter · esc")
	}

	input := "   " + styDim.Render("name") + "  " + m.zipInput.View()
	preview := "   " + styDim.Render("→ ") + styDim.Render(elidePath(shortPath(m.zipPreview()), max(8, m.width-6)))
	return []string{line1, input, preview}
}

// zipPreview resolves the real target so the preview matches what enter will
// create, including a non-clobbering "-2" suffix when the name exists.
func (m Model) zipPreview() string {
	name := strings.TrimSpace(m.zipInput.Value())
	target, err := resolveArchiveTarget(name, m.dir, m.zipDefault)
	if err != nil {
		return m.zipDefault
	}
	return uniquePath(target)
}

// ----------------------------------------------------------------- progress

func (m Model) viewProgress() string {
	p := m.opProgress
	lines := []string{m.pageHeader(m.opKind.progressTitle(), ""), m.rule(), ""}

	bw := clampInt(m.width-8, 10, 60)
	switch m.opKind {
	case actZip:
		lines = append(lines, "  "+styStrong.Render(clip(shortPath(m.opTarget), max(8, m.width-4))), "")
		if p.scanning {
			lines = append(lines,
				"  "+indeterminateBar(m.spin, bw),
				"  "+styDim.Render(spinner(m.spin)+" collecting files…"),
			)
		} else {
			pct := 0.0
			if p.total > 0 {
				pct = float64(p.done) / float64(p.total)
				if pct > 1 {
					pct = 1
				}
			}
			lines = append(lines,
				"  "+thinBar(pct, bw)+"  "+styStrong.Render(fmt.Sprintf("%3.0f%%", pct*100)),
				"",
				"  "+styDim.Render(clip(humanSize(p.done)+" / "+humanSize(p.total), max(8, m.width-4))),
				"  "+styDim.Render(clip(fmt.Sprintf("%d / %d files", p.filesDone, p.filesTotal), max(8, m.width-4))),
			)
		}
	case actTrash, actDelete:
		verb := "Deleting"
		amountWord := "freed"
		if m.opKind == actTrash {
			verb = "Moving to Trash"
			amountWord = "total"
			if p.scanning {
				amountWord = "measured"
			}
		}
		title := fmt.Sprintf("%s %s", verb, countNoun(len(normalizeRoots(m.deleteTargets)), "item", "items"))
		metric := spinner(m.spin) + " " + humanSize(p.done) + " " + amountWord + " · " + countNoun(p.filesDone, "file", "files")
		lines = append(lines,
			"  "+styStrong.Render(clip(title, max(8, m.width-4))),
			"",
			"  "+indeterminateBar(m.spin, bw),
			"  "+styDim.Render(clip(metric, max(8, m.width-4))),
		)
	}

	if p.current != "" {
		lines = append(lines, "", "  "+styDim.Render(clip(p.current, max(8, m.width-4))))
	}
	lines = append(lines, "")

	if m.status != "" {
		mark := "· "
		style := styWarn
		if m.statusErr {
			mark = "✗ "
			style = styBad
		}
		lines = append(lines, "  "+style.Render(clip(mark+m.status, max(8, m.width-4))))
	} else {
		lines = append(lines, "  "+styDim.Render("elapsed "+humanDuration(time.Since(m.opStarted))))
	}
	lines = append(lines, keyHints([][2]string{{"esc", "cancel"}}, m.width))
	return m.fit(lines)
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Round(time.Second).Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh%02dm", s/3600, (s%3600)/60)
}

// ------------------------------------------------------------------ summary

func (m Model) viewSummary() string {
	r := m.result
	title := r.action.doneTitle(r.canceled)
	if !r.canceled && r.roots == 0 && len(r.problems) > 0 && (r.action == actTrash || r.action == actDelete) {
		title = "Finished with problems"
	}
	lines := []string{m.pageHeader("done", ""), m.rule(), ""}
	switch {
	case r.canceled:
		lines = append(lines, "  "+styWarn.Render(title))
	case len(r.problems) > 0:
		lines = append(lines, "  "+styWarn.Render(title))
	default:
		lines = append(lines, "  "+styGood.Render(title))
	}
	lines = append(lines, "")

	switch r.action {
	case actZip:
		if r.outBytes > 0 {
			plainSizes := "  " + humanSize(r.bytes) + "  →  " + humanSize(r.outBytes)
			line := "  " + styStrong.Render(humanSize(r.bytes)) + styDim.Render("  →  ") +
				styStrong.Render(humanSize(r.outBytes))
			savedText := ""
			if r.outBytes < r.bytes && r.bytes > 0 {
				saved := r.bytes - r.outBytes
				pct := float64(saved) / float64(r.bytes) * 100
				savedText = fmt.Sprintf("saved %s · %.0f%%", humanSize(saved), pct)
			}
			if savedText != "" && visibleLen(plainSizes)+len(savedText)+5 <= m.width {
				line += "   " + styGood.Render(savedText)
				lines = append(lines, line, "")
			} else {
				lines = append(lines, line)
				if savedText != "" {
					lines = append(lines, "  "+styGood.Render(savedText))
				} else if r.bytes > 0 {
					lines = append(lines, "  "+styWarn.Render("no size saved"))
				}
				lines = append(lines, "")
			}
		}
		if !r.canceled {
			lines = append(lines,
				"  "+styDim.Render(clip(countNoun(r.files, "file", "files")+" · "+countNoun(r.dirs, "folder", "folders"), max(8, m.width-4))),
			)
		} else {
			lines = append(lines, "  "+styDim.Render("archive not created"))
		}
		if r.path != "" {
			lines = append(lines, "", "  "+styDim.Render("output"))
			for _, l := range wrapHard(shortPath(r.path), max(8, m.width-4)) {
				lines = append(lines, "  "+l)
			}
		}

	case actTrash, actDelete:
		sizeLine := "  " + styStrong.Render(countNoun(r.roots, "item", "items")) +
			styDim.Render("  ·  ") + styStrong.Render(humanSize(r.bytes))
		verb := ""
		if r.action == actDelete {
			verb = "freed"
			if visibleLen(countNoun(r.roots, "item", "items")+"  ·  "+humanSize(r.bytes))+len(verb)+4 <= m.width-2 {
				sizeLine += " " + styDim.Render(verb)
				verb = ""
			}
		}
		lines = append(lines, sizeLine)
		if verb != "" {
			lines = append(lines, "  "+styDim.Render(verb))
		}
		lines = append(lines, "",
			"  "+styDim.Render(clip(countNoun(r.files, "file", "files")+" · "+countNoun(r.dirs, "folder", "folders"), max(8, m.width-4))),
		)
		if r.canceled {
			note := "nothing was changed"
			if r.roots > 0 || r.bytes > 0 {
				note = "partial changes were made"
			}
			lines = append(lines, "  "+styDim.Render(note))
		}
		if r.action == actTrash && r.openDir != "" {
			lines = append(lines, "", "  "+styDim.Render("in "+shortPath(r.openDir)))
		}
	}

	if r.elapsed > 0 {
		lines = append(lines, "", "  "+styDim.Render("took "+humanDuration(r.elapsed)))
	}

	if len(r.problems) > 0 {
		lines = append(lines, "", "  "+styDim.Render("NOTES"))
		for i, p := range r.problems {
			if i >= 6 {
				lines = append(lines, "  "+styDim.Render(fmt.Sprintf("… and %d more", len(r.problems)-6)))
				break
			}
			for _, part := range wrapWords(p, max(8, m.width-4)) {
				lines = append(lines, "  "+styWarn.Render(part))
			}
		}
	}

	lines = append(lines, "")
	lines = append(lines, keyHints([][2]string{{"enter", "back to files"}, {"o", "open folder"}, {"q", "quit"}}, m.width))
	return m.fit(lines)
}

// --------------------------------------------------------------------- help

func (m Model) viewHelp() string {
	if m.height < 27 || m.width < 72 {
		return m.viewHelpCompact()
	}

	lines := []string{
		m.pageHeader("help", "press any key to close"),
		m.rule(),
		"",
	}

	section := func(title string, paragraphs ...string) {
		lines = append(lines, "  "+accent(title))
		for _, p := range paragraphs {
			for _, l := range wrapWords(p, max(10, m.width-4)) {
				lines = append(lines, "  "+styDim.Render(l))
			}
		}
		lines = append(lines, "")
	}

	section("NAVIGATE",
		"↑ ↓ or k j move · PgUp PgDn page · g G first last · → l enter open",
		"← h backspace back · ~ home · o open folder · ctrl+r rescan",
	)
	section("VIEW",
		"/ filter · s sort · S reverse · H hidden files · esc clear",
	)
	section("SELECT",
		"space select · a all visible · x deselect everything in view · n clear",
	)
	section("ACTIONS",
		"d delete — move to Trash or delete permanently · z create a zip archive",
	)
	section("ABOUT",
		"Folder sizes are recursive and count every file type, including hidden files.",
		"? help · q quit · Q quit with a selection",
	)

	lines = append(lines, keyHints([][2]string{{"esc", "back"}}, m.width))
	return m.fit(lines)
}

// viewHelpCompact keeps the whole reference usable on a short or narrow
// terminal. The full layout is prettier, but a help screen that hides half its
// keys is not helpful.
func (m Model) viewHelpCompact() string {
	lines := []string{
		m.pageHeader("help", "any key closes"),
		m.rule(),
		"",
	}
	section := func(title, text string) {
		lines = append(lines, "  "+accent(title))
		for _, l := range wrapWords(text, max(10, m.width-4)) {
			lines = append(lines, "  "+styDim.Render(l))
		}
	}
	section("NAVIGATE",
		"↑↓ or k j move · PgUp PgDn page · g G first last · enter open · ← h back · ~ home · o open folder · ctrl+r rescan")
	section("VIEW", "/ filter · s sort · S reverse · H hidden · esc clear")
	section("SELECT", "space select · a all visible · x deselect view · n clear")
	section("ACTIONS", "d delete (Trash or permanent) · z zip")
	lines = append(lines, "")
	for _, l := range wrapWords("Every size is recursive and counts all file types.", max(10, m.width-4)) {
		lines = append(lines, "  "+styDim.Render(l))
	}
	lines = append(lines, keyHints([][2]string{{"esc", "back"}}, m.width))
	return m.fit(lines)
}
