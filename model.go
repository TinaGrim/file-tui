package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type screen int

const (
	scrBrowse screen = iota
	scrHelp
	scrProgress
	scrSummary
)

type overlay int

const (
	ovNone overlay = iota
	ovDelete
	ovZipPrompt
)

const (
	sizeTTL     = 5 * time.Minute
	maxSizeJobs = 4
)

type cachedSize struct {
	info sizeInfo
	at   time.Time
}

type scanMsg struct{ res scanned }

type sizeMsg struct {
	path string
	info sizeInfo
	gen  int
}

type tickMsg time.Time

type Model struct {
	screen   screen
	overlay  overlay
	width    int
	height   int
	ready    bool
	quitting bool

	dir     string
	entries []Entry
	visible []int
	cursor  int
	top     int
	loading bool
	loadErr error

	focusPath  string
	focusIndex int

	sel      map[string]bool
	selDirs  map[string]bool
	selSizes map[string]int64
	selOrder []string

	sort       sortMode
	reverse    bool
	filter     string
	showHidden bool

	filterInput textinput.Model
	zipInput    textinput.Model
	editing     int // 0 none, 1 filter

	sizes        map[string]cachedSize
	sizeBusy     map[string]bool
	sizeInFlight int
	sizeGen      int

	status    string
	statusErr bool
	statusTTL int
	spin      int

	deleteTargets []string
	zipRoots      []string
	zipDefault    string

	opKind     actionKind
	opProgress opProgressMsg
	opStarted  time.Time
	opCancel   context.CancelFunc
	opCh       chan opProgressMsg
	opTarget   string

	result  opResult
	trashOK bool
}

func NewModel(start string, preselect []string) Model {
	fi := textinput.New()
	fi.Prompt = ""
	fi.Placeholder = "name contains…"
	fi.CharLimit = 120

	zi := textinput.New()
	zi.Prompt = ""
	zi.CharLimit = 200

	if start == "" {
		if wd, err := os.Getwd(); err == nil {
			start = wd
		} else {
			start = homeDir()
		}
	}
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	if st, err := os.Stat(start); err == nil && !st.IsDir() {
		start = filepath.Dir(start)
	}

	m := Model{
		screen:      scrBrowse,
		dir:         start,
		sel:         map[string]bool{},
		selDirs:     map[string]bool{},
		selSizes:    map[string]int64{},
		filterInput: fi,
		zipInput:    zi,
		sizes:       map[string]cachedSize{},
		sizeBusy:    map[string]bool{},
		trashOK:     trashSupported(),
	}

	for _, p := range preselect {
		abs, err := filepath.Abs(expandHome(p))
		if err != nil {
			continue
		}
		st, err := os.Stat(abs)
		if err != nil {
			continue
		}
		m.addSelectedPath(abs, st.IsDir(), st.Size())
		if m.focusPath == "" {
			m.focusPath = abs
		}
	}
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.scan(m.focusPath, m.focusIndex), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) scan(focus string, index int) tea.Cmd {
	dir := m.dir
	return func() tea.Msg {
		list, err := listDir(dir)
		return scanMsg{res: scanned{dir: dir, focus: focus, index: index, list: list, err: err}}
	}
}

func sizeCmd(path string, gen int) tea.Cmd {
	return func() tea.Msg {
		return sizeMsg{path: path, info: measure(path), gen: gen}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.filterInput.Width = max(10, msg.Width-18)
		m.zipInput.Width = max(10, msg.Width-22)
		m.clampScroll()
		return m, nil

	case tickMsg:
		m.spin++
		if m.statusTTL > 0 {
			m.statusTTL--
			if m.statusTTL == 0 {
				m.status, m.statusErr = "", false
			}
		}
		cmds := []tea.Cmd{tickCmd()}
		if m.screen == scrBrowse && m.overlay != ovZipPrompt {
			if c := m.wantSizes(); c != nil {
				cmds = append(cmds, c)
			}
		}
		return m, tea.Batch(cmds...)

	case scanMsg:
		if msg.res.dir != m.dir {
			return m, nil
		}
		m.loading = false
		if msg.res.err != nil {
			m.entries = nil
			m.visible = nil
			m.cursor, m.top = 0, 0
			m.loadErr = msg.res.err
			m.status, m.statusErr, m.statusTTL = "", false, 0
			return m, nil
		}
		m.loadErr = nil
		m.entries = msg.res.list
		m.reindex()
		m.restoreCursor(msg.res.focus, msg.res.index)
		return m, m.wantSizes()

	case sizeMsg:
		if m.sizeInFlight > 0 {
			m.sizeInFlight--
		}
		delete(m.sizeBusy, msg.path)
		if msg.gen == m.sizeGen {
			m.sizes[msg.path] = cachedSize{info: msg.info, at: time.Now()}
			if m.selDirs[msg.path] && msg.info.done {
				if msg.info.err == "" {
					m.selSizes[msg.path] = msg.info.bytes
				} else {
					m.selSizes[msg.path] = 0
				}
			}
			// A size-sorted list should settle into the right order as
			// measurements arrive, without moving the row under the cursor.
			if m.sort == sortSize {
				m.reindexKeepingCursor()
			}
		}
		return m, nil

	case opProgressMsg:
		m.opProgress = msg
		if m.opCh != nil {
			return m, waitProgress(m.opCh)
		}
		return m, nil

	case operationDoneMsg:
		return m.applyOperationDone(msg.res)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) say(text string, isErr bool) {
	m.status, m.statusErr, m.statusTTL = text, isErr, 45
}

// ------------------------------------------------------------------ list ops

func (m *Model) reindex() {
	sortEntries(m.entries, m.sort, m.reverse, func(e Entry) int64 {
		if e.Dir {
			if c, ok := m.sizes[e.Path]; ok && c.info.done && c.info.err == "" {
				return c.info.bytes
			}
			return 0
		}
		return e.Size
	})

	needle := strings.ToLower(strings.TrimSpace(m.filter))
	m.visible = m.visible[:0]
	for i := range m.entries {
		e := m.entries[i]
		if !e.IsParent() {
			if !m.showHidden && e.Hidden {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(e.Name), needle) {
				continue
			}
		}
		m.visible = append(m.visible, i)
	}
	if len(m.visible) == 0 {
		m.cursor = 0
	} else {
		m.cursor = clampInt(m.cursor, 0, len(m.visible)-1)
	}
	m.clampScroll()
}

func (m *Model) reindexKeepingCursor() {
	focus := ""
	if e, ok := m.cur(); ok {
		focus = e.Path
	}
	index := m.cursor
	m.reindex()
	m.restoreCursor(focus, index)
}

func (m *Model) restoreCursor(path string, index int) {
	if path != "" {
		for i, idx := range m.visible {
			if m.entries[idx].Path == path {
				m.cursor = i
				m.clampScroll()
				return
			}
		}
	}
	if path == "" && index == 0 && len(m.visible) > 1 {
		if m.entries[m.visible[0]].IsParent() {
			m.cursor = 1
			m.clampScroll()
			return
		}
	}
	if len(m.visible) > 0 {
		m.cursor = clampInt(index, 0, len(m.visible)-1)
	} else {
		m.cursor = 0
	}
	m.clampScroll()
}

func (m Model) cur() (Entry, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return Entry{}, false
	}
	return m.entries[m.visible[m.cursor]], true
}

func (m Model) listHeight() int {
	if m.height <= 0 {
		return 10
	}
	h := m.height - 6
	if m.overlay != ovNone {
		h--
	}
	return max(3, h)
}

func (m *Model) clampScroll() {
	win := m.listHeight()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+win {
		m.top = m.cursor - win + 1
	}
	if m.top < 0 {
		m.top = 0
	}
	if maxTop := max(0, len(m.visible)-win); m.top > maxTop {
		m.top = maxTop
	}
}

func (m *Model) move(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.cursor = clampInt(m.cursor+delta, 0, len(m.visible)-1)
	m.clampScroll()
}

func (m *Model) jump(delta int) {
	m.move(delta * m.listHeight())
}

// ---------------------------------------------------------------- selection

func (m *Model) addSelectedPath(path string, isDir bool, size int64) {
	if path == "" || m.sel[path] {
		return
	}
	// A selected folder already covers everything inside it. Keep the
	// selection free of duplicates so the count, the delete dialog and the
	// actual operation always agree.
	if m.coveredBySelected(path) {
		return
	}
	if isDir {
		keep := m.selOrder[:0]
		for _, p := range m.selOrder {
			if isWithin(p, path) {
				delete(m.sel, p)
				delete(m.selDirs, p)
				delete(m.selSizes, p)
				continue
			}
			keep = append(keep, p)
		}
		m.selOrder = keep
	}
	m.sel[path] = true
	m.selDirs[path] = isDir
	m.selSizes[path] = size
	m.selOrder = append(m.selOrder, path)
}

func (m *Model) removeSelected(path string) {
	if !m.sel[path] {
		return
	}
	delete(m.sel, path)
	delete(m.selDirs, path)
	delete(m.selSizes, path)
	for i, p := range m.selOrder {
		if p == path {
			m.selOrder = append(m.selOrder[:i], m.selOrder[i+1:]...)
			break
		}
	}
}

func (m Model) coveredBySelected(path string) bool {
	p := filepath.Clean(path)
	for {
		if m.selDirs[p] {
			return true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return false
		}
		p = parent
	}
}

func (m *Model) toggle(e Entry) {
	if e.IsParent() {
		return
	}
	if m.sel[e.Path] {
		m.removeSelected(e.Path)
		return
	}
	if m.coveredBySelected(e.Path) {
		m.say("already covered by a selected folder", false)
		return
	}
	m.addSelectedPath(e.Path, e.Dir, e.Size)
}

func (m *Model) clearSelection() {
	m.sel = map[string]bool{}
	m.selDirs = map[string]bool{}
	m.selSizes = map[string]int64{}
	m.selOrder = nil
}

func (m Model) selectedTotal() (int, int64, bool, bool) {
	var bytes int64
	pending := false
	unknown := false
	for _, p := range m.selOrder {
		if m.selDirs[p] {
			if c, ok := m.sizes[p]; ok && c.info.done {
				if c.info.err == "" {
					bytes += c.info.bytes
				} else {
					unknown = true
				}
			} else {
				pending = true
			}
			continue
		}
		bytes += m.selSizes[p]
	}
	return len(m.selOrder), bytes, pending, unknown
}

func (m Model) operationTargets() []string {
	if len(m.selOrder) > 0 {
		return normalizeRoots(m.selOrder)
	}
	if e, ok := m.cur(); ok && !e.IsParent() {
		return []string{e.Path}
	}
	return nil
}

func (m *Model) forgetPaths(targets []string) {
	drop := make(map[string]bool, len(targets))
	for _, t := range targets {
		drop[filepath.Clean(t)] = true
	}
	keep := m.selOrder[:0]
	for _, p := range m.selOrder {
		remove := false
		q := filepath.Clean(p)
		for {
			if drop[q] {
				remove = true
				break
			}
			parent := filepath.Dir(q)
			if parent == q {
				break
			}
			q = parent
		}
		if remove {
			delete(m.sel, p)
			delete(m.selDirs, p)
			delete(m.selSizes, p)
			continue
		}
		keep = append(keep, p)
	}
	m.selOrder = keep
}

// ------------------------------------------------------------------- sizing

func (m *Model) wantSizes() tea.Cmd {
	if m.sizeInFlight >= maxSizeJobs || m.loading {
		return nil
	}
	seen := map[string]bool{}
	var jobs []string

	add := func(path string, isDir bool) {
		if !isDir || path == "" || seen[path] || m.sizeBusy[path] {
			return
		}
		if c, ok := m.sizes[path]; ok {
			if c.info.done && time.Since(c.at) < sizeTTL {
				return
			}
		}
		seen[path] = true
		jobs = append(jobs, path)
	}

	for _, p := range m.selOrder {
		if m.selDirs[p] {
			add(p, true)
		}
	}
	end := min(m.top+m.listHeight(), len(m.visible))
	for i := m.top; i < end; i++ {
		e := m.entries[m.visible[i]]
		if e.Dir && !e.IsParent() {
			add(e.Path, true)
		}
	}
	// The current folder total is last: it can be the most expensive walk.
	add(m.dir, true)

	slots := maxSizeJobs - m.sizeInFlight
	if len(jobs) > slots {
		jobs = jobs[:slots]
	}
	if len(jobs) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(jobs))
	for _, p := range jobs {
		m.sizeBusy[p] = true
		m.sizeInFlight++
		cmds = append(cmds, sizeCmd(p, m.sizeGen))
	}
	return tea.Batch(cmds...)
}

func (m *Model) invalidateSizes() {
	m.sizes = map[string]cachedSize{}
	m.sizeGen++
}

// -------------------------------------------------------------------- input

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		if m.screen == scrProgress {
			m.cancelOperation()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	}

	switch m.overlay {
	case ovDelete:
		return m.handleDeleteConfirm(key)
	case ovZipPrompt:
		return m.updateZipInput(msg)
	}

	if m.editing == 1 {
		return m.updateFilter(msg)
	}

	switch m.screen {
	case scrBrowse:
		return m.updateBrowse(key)
	case scrHelp:
		m.screen = scrBrowse
		return m, nil
	case scrProgress:
		return m.updateProgress(key)
	case scrSummary:
		return m.updateSummary(key)
	}
	return m, nil
}

func (m Model) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.editing = 0
		m.filter = ""
		m.filterInput.SetValue("")
		m.filterInput.Blur()
		m.reindexKeepingCursor()
		return m, m.wantSizes()
	case "enter":
		m.editing = 0
		m.filter = m.filterInput.Value()
		m.filterInput.Blur()
		m.reindexKeepingCursor()
		return m, m.wantSizes()
	}
	var cmd tea.Cmd
	m.filterInput, cmd = m.filterInput.Update(msg)
	m.filter = m.filterInput.Value()
	m.reindexKeepingCursor()
	return m, tea.Batch(cmd, m.wantSizes())
}

func (m Model) updateBrowse(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q":
		if len(m.selOrder) > 0 {
			m.say("selection kept · press shift+q to quit anyway", false)
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit

	case "Q":
		m.quitting = true
		return m, tea.Quit

	case "esc":
		if m.filter != "" {
			m.filter = ""
			m.filterInput.SetValue("")
			m.reindexKeepingCursor()
			return m, m.wantSizes()
		}
		if len(m.selOrder) > 0 {
			m.clearSelection()
			return m, nil
		}
		m.say("press q to quit", false)
		return m, nil

	case "up", "k":
		m.move(-1)
		return m, m.wantSizes()
	case "down", "j":
		m.move(1)
		return m, m.wantSizes()
	case "pgup":
		m.jump(-1)
		return m, m.wantSizes()
	case "pgdown":
		m.jump(1)
		return m, m.wantSizes()
	case "home", "g":
		m.cursor = 0
		m.clampScroll()
		return m, m.wantSizes()
	case "end", "G":
		m.cursor = max(0, len(m.visible)-1)
		m.clampScroll()
		return m, m.wantSizes()

	case "right", "l", "enter":
		e, ok := m.cur()
		if !ok {
			return m, nil
		}
		if e.Dir {
			return m.enterDir(e.Path)
		}
		if err := openPath(e.Path); err != nil {
			m.say("could not open: "+err.Error(), true)
		}
		return m, nil

	case "left", "h", "backspace":
		return m.enterDir(filepath.Dir(m.dir))

	case " ":
		e, ok := m.cur()
		if !ok {
			return m, nil
		}
		if e.IsParent() {
			return m.enterDir(e.Path)
		}
		m.toggle(e)
		return m, m.wantSizes()

	case "a":
		for _, idx := range m.visible {
			e := m.entries[idx]
			if !e.IsParent() {
				m.addSelectedPath(e.Path, e.Dir, e.Size)
			}
		}
		return m, m.wantSizes()

	case "n":
		m.clearSelection()
		m.say("selection cleared", false)
		return m, nil

	case "x":
		drop := make(map[string]bool)
		for _, idx := range m.visible {
			drop[m.entries[idx].Path] = true
		}
		keep := m.selOrder[:0]
		for _, p := range m.selOrder {
			if drop[p] {
				delete(m.sel, p)
				delete(m.selDirs, p)
				delete(m.selSizes, p)
				continue
			}
			keep = append(keep, p)
		}
		m.selOrder = keep
		m.say("deselected everything in view", false)
		return m, nil

	case "/":
		m.editing = 1
		m.filterInput.SetValue(m.filter)
		m.filterInput.CursorEnd()
		m.status, m.statusErr, m.statusTTL = "", false, 0
		return m, m.filterInput.Focus()

	case "s":
		m.sort = sortMode(wrap(int(m.sort)+1, 4))
		m.reindexKeepingCursor()
		m.say("sorted by "+m.sort.String()+" · "+sortDirection(m.sort, m.reverse), false)
		return m, m.wantSizes()

	case "S":
		m.reverse = !m.reverse
		m.reindexKeepingCursor()
		m.say("sorted by "+m.sort.String()+" · "+sortDirection(m.sort, m.reverse), false)
		return m, m.wantSizes()

	case "H":
		m.showHidden = !m.showHidden
		m.reindexKeepingCursor()
		if m.showHidden {
			m.say("hidden files shown", false)
		} else {
			m.say("hidden files hidden", false)
		}
		return m, m.wantSizes()

	case "d":
		return m.askDelete()

	case "z":
		return m.askZip()

	case "o":
		if err := openPath(m.dir); err != nil {
			m.say("could not open folder: "+err.Error(), true)
		}
		return m, nil

	case "~":
		return m.enterDir(homeDir())

	case "ctrl+r":
		m.invalidateSizes()
		m.loading = true
		e, ok := m.cur()
		focus := ""
		if ok {
			focus = e.Path
		}
		return m, m.scan(focus, m.cursor)

	case "?":
		m.screen = scrHelp
		return m, nil
	}
	return m, nil
}

func sortDirection(mode sortMode, reverse bool) string {
	desc := mode.descendingByDefault()
	if reverse {
		desc = !desc
	}
	if desc {
		return "descending"
	}
	return "ascending"
}

func (m Model) enterDir(dir string) (tea.Model, tea.Cmd) {
	if dir == "" {
		return m, nil
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if dir == m.dir {
		return m, nil
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		m.say("cannot open "+shortPath(dir), true)
		return m, nil
	}
	m.dir = dir
	m.cursor, m.top = 0, 0
	m.loading = true
	m.loadErr = nil
	m.filter = ""
	m.filterInput.SetValue("")
	m.entries = nil
	m.visible = nil
	m.focusPath = ""
	m.focusIndex = 0
	m.status, m.statusErr, m.statusTTL = "", false, 0
	return m, m.scan("", 0)
}

// ------------------------------------------------------------------- delete

func (m Model) askDelete() (tea.Model, tea.Cmd) {
	targets := m.operationTargets()
	if len(targets) == 0 {
		m.say("nothing to delete", true)
		return m, nil
	}
	for _, p := range targets {
		if err := guardDelete(p); err != nil {
			m.say(err.Error(), true)
			return m, nil
		}
	}
	m.deleteTargets = targets
	m.overlay = ovDelete
	m.status, m.statusErr, m.statusTTL = "", false, 0
	return m, m.wantSizes()
}

func (m Model) handleDeleteConfirm(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "t", "y":
		if !m.trashOK {
			m.say("Trash is not available here — use shift+d to delete permanently", true)
			return m, nil
		}
		return m.startDelete(true)
	case "D":
		return m.startDelete(false)
	case "esc", "q", "n":
		m.overlay = ovNone
		m.say("delete canceled", false)
		return m, nil
	}
	return m, nil
}

func (m Model) startDelete(trash bool) (tea.Model, tea.Cmd) {
	targets := m.deleteTargets
	m.overlay = ovNone
	if len(targets) == 0 {
		m.say("nothing to delete", true)
		return m, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan opProgressMsg, 64)
	m.opKind = actDelete
	if trash {
		m.opKind = actTrash
	}
	m.opProgress = opProgressMsg{scanning: true, current: "preparing…"}
	m.opStarted = time.Now()
	m.opCancel = cancel
	m.opCh = ch
	m.opTarget = ""
	m.status, m.statusErr, m.statusTTL = "", false, 0
	m.screen = scrProgress

	return m, tea.Batch(deleteCommand(ctx, targets, trash, m.dir, ch), waitProgress(ch))
}

// ---------------------------------------------------------------------- zip

func (m Model) askZip() (tea.Model, tea.Cmd) {
	targets := m.operationTargets()
	if len(targets) == 0 {
		m.say("nothing to zip", true)
		return m, nil
	}
	for _, p := range targets {
		clean := filepath.Clean(p)
		if filepath.Dir(clean) == clean {
			m.say("refusing to zip the filesystem root", true)
			return m, nil
		}
	}
	m.zipRoots = targets
	m.zipDefault = defaultArchiveName(m.dir, targets)
	m.zipInput.SetValue("")
	m.zipInput.Placeholder = m.zipDefault
	m.zipInput.CursorEnd()
	m.overlay = ovZipPrompt
	m.status, m.statusErr, m.statusTTL = "", false, 0
	return m, m.zipInput.Focus()
}

func (m Model) zipPromptTarget() (string, error) {
	name := strings.TrimSpace(m.zipInput.Value())
	if name == "" {
		name = m.zipDefault
	}
	target, err := resolveArchiveTarget(name, m.dir, m.zipDefault)
	if err != nil {
		return "", err
	}
	return uniquePath(target), nil
}

func (m Model) updateZipInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.overlay = ovNone
		m.zipInput.Blur()
		m.say("zip canceled", false)
		return m, nil
	case "enter":
		target, err := m.zipPromptTarget()
		if err != nil {
			m.say(err.Error(), true)
			return m, nil
		}
		m.overlay = ovNone
		m.zipInput.Blur()
		return m.startZip(target)
	}
	var cmd tea.Cmd
	m.zipInput, cmd = m.zipInput.Update(msg)
	return m, cmd
}

func (m Model) startZip(target string) (tea.Model, tea.Cmd) {
	if len(m.zipRoots) == 0 {
		m.say("nothing to zip", true)
		return m, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan opProgressMsg, 64)
	m.opKind = actZip
	m.opProgress = opProgressMsg{scanning: true, current: "collecting files…"}
	m.opStarted = time.Now()
	m.opCancel = cancel
	m.opCh = ch
	m.opTarget = target
	m.status, m.statusErr, m.statusTTL = "", false, 0
	m.screen = scrProgress

	return m, tea.Batch(zipCommand(ctx, m.zipRoots, target, ch), waitProgress(ch))
}

// ----------------------------------------------------------------- operations

func (m Model) applyOperationDone(res opResult) (tea.Model, tea.Cmd) {
	m.opCancel = nil
	m.opCh = nil
	m.invalidateSizes()

	if res.fatal != "" {
		m.screen = scrBrowse
		m.say(res.fatal, true)
		m.loading = true
		focus := ""
		if e, ok := m.cur(); ok {
			focus = e.Path
		}
		return m, m.scan(focus, m.cursor)
	}

	if res.action == actTrash || res.action == actDelete {
		if len(res.completed) > 0 {
			m.forgetPaths(res.completed)
		}
	}
	m.result = res
	m.screen = scrSummary
	m.status, m.statusErr, m.statusTTL = "", false, 0

	m.loading = true
	focus := ""
	if e, ok := m.cur(); ok {
		focus = e.Path
	}
	index := m.cursor
	if res.action == actZip && !res.canceled && res.path != "" && filepath.Dir(res.path) == m.dir {
		focus = res.path
		index = 0
	}
	return m, m.scan(focus, index)
}

func (m *Model) cancelOperation() {
	if m.opCancel != nil {
		m.opCancel()
		m.say("cancelling…", false)
	}
}

func (m Model) updateProgress(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "ctrl+c":
		m.cancelOperation()
	}
	return m, nil
}

func (m Model) updateSummary(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "esc", "enter":
		if m.result.action == actZip && !m.result.canceled {
			m.clearSelection()
			m.say("archive created · selection cleared", false)
		}
		m.screen = scrBrowse
		return m, m.wantSizes()
	case "o":
		dir := m.result.openDir
		if dir == "" {
			dir = m.dir
		}
		if err := openPath(dir); err != nil {
			m.say("could not open folder: "+err.Error(), true)
		}
		return m, nil
	case "q":
		m.quitting = true
		return m, tea.Quit
	}
	return m, nil
}

// --------------------------------------------------------------------- open

func openPath(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// ---------------------------------------------------------------- formatting

func (m Model) statusLineText() string {
	if m.status != "" {
		return m.status
	}
	items := 0
	for _, e := range m.entries {
		if !e.IsParent() && (m.showHidden || !e.Hidden) {
			items++
		}
	}
	left := countNoun(items, "item", "items")
	if m.filter != "" {
		shown := 0
		for _, idx := range m.visible {
			if !m.entries[idx].IsParent() {
				shown++
			}
		}
		left = fmt.Sprintf("%d of %d items · filter %q", shown, items, m.filter)
	}
	if m.coveredBySelected(m.dir) {
		left += " · inside a selected folder"
	}
	if m.loading {
		left += " · " + spinner(m.spin) + " scanning"
	}
	if m.loadErr != nil {
		left = m.loadErr.Error()
	}
	return left
}

func (m Model) selectionSummary() string {
	count, bytes, pending, unknown := m.selectedTotal()
	if count == 0 {
		return ""
	}
	label := countNoun(count, "selected", "selected")
	size := humanSize(bytes)
	switch {
	case pending:
		if bytes > 0 {
			size += "+"
		} else {
			size = "measuring…"
		}
	case unknown:
		if bytes > 0 {
			size += "+"
		} else {
			size = "size unavailable"
		}
	}
	return label + " · " + size
}

func (m Model) folderSummary() string {
	if m.loadErr != nil {
		return ""
	}
	c, ok := m.sizes[m.dir]
	if !ok || !c.info.done {
		return spinner(m.spin) + " measuring total"
	}
	if c.info.err != "" {
		return "total unavailable"
	}
	size := humanSize(c.info.bytes)
	if c.info.errs > 0 {
		size = "~" + size
	}
	parts := []string{size}
	if c.info.files > 0 {
		parts = append(parts, countNoun(c.info.files, "file", "files"))
	}
	return strings.Join(parts, " · ")
}

func (m Model) deleteModalSize() string {
	var total int64
	pending := false
	for _, p := range m.deleteTargets {
		size, known := m.knownSizeFor(p)
		if known {
			total += size
		} else {
			pending = true
		}
	}
	if total == 0 && pending {
		return "measuring…"
	}
	s := humanSize(total)
	if pending {
		s += "+"
	}
	return s
}

// knownSizeFor looks up a target without touching the filesystem. The browser
// already has file sizes and the background measurer fills in folders.
func (m Model) knownSizeFor(p string) (int64, bool) {
	if c, ok := m.sizes[p]; ok && c.info.done {
		if c.info.err == "" {
			return c.info.bytes, true
		}
		return 0, true
	}
	if m.selDirs[p] {
		return 0, false
	}
	if sz, ok := m.selSizes[p]; ok {
		return sz, true
	}
	for _, e := range m.entries {
		if e.Path == p && !e.Dir {
			return e.Size, true
		}
	}
	if e, ok := m.cur(); ok && e.Path == p {
		if e.Dir {
			return 0, false
		}
		return e.Size, true
	}
	return 0, false
}
