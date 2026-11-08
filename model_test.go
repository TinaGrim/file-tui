package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testModel(t *testing.T) Model {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "document.pdf"), "1234567890")
	writeTestFile(t, filepath.Join(root, "archive.zip"), "12345")
	writeTestFile(t, filepath.Join(root, ".hidden"), "h")
	if err := os.MkdirAll(filepath.Join(root, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := NewModel(root, nil)
	m.width, m.height, m.ready = 120, 30, true
	return m
}

func loadScan(t *testing.T, m Model) Model {
	t.Helper()
	mm, _ := m.Update(m.scan("", 0)())
	got, ok := mm.(Model)
	if !ok {
		t.Fatalf("expected Model, got %T", mm)
	}
	return got
}

func press(m Model, msg tea.Msg) Model {
	mm, _ := m.Update(msg)
	return mm.(Model)
}

func runeKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestInitialCursorSkipsParent(t *testing.T) {
	m := loadScan(t, testModel(t))
	e, ok := m.cur()
	if !ok {
		t.Fatal("no current entry")
	}
	if e.IsParent() {
		t.Fatalf("cursor started on parent: %+v", e)
	}
}

func TestSelectionAndDeleteDialog(t *testing.T) {
	m := loadScan(t, testModel(t))
	m = press(m, runeKey(" "))
	if len(m.selOrder) != 1 {
		t.Fatalf("selected %d, want 1", len(m.selOrder))
	}
	m = press(m, runeKey("a"))
	// Hidden files are not visible, so "all here" selects the three visible
	// files/folders but never the hidden one or the parent row.
	if len(m.selOrder) != 3 {
		t.Fatalf("selected %d, want 3: %v", len(m.selOrder), m.selOrder)
	}
	m = press(m, runeKey("d"))
	if m.overlay != ovDelete {
		t.Fatalf("overlay = %v, want delete", m.overlay)
	}
	if len(m.deleteTargets) != 3 {
		t.Fatalf("delete targets = %v", m.deleteTargets)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay != ovNone {
		t.Fatal("esc did not close delete dialog")
	}
	m = press(m, runeKey("n"))
	if len(m.selOrder) != 0 {
		t.Fatal("n did not clear selection")
	}
}

func TestFilterAndHiddenToggle(t *testing.T) {
	m := loadScan(t, testModel(t))
	m = press(m, runeKey("/"))
	m = press(m, runeKey("p"))
	m = press(m, runeKey("d"))
	m = press(m, runeKey("f"))
	if m.filter != "pdf" {
		t.Fatalf("filter = %q", m.filter)
	}
	shown := 0
	for _, idx := range m.visible {
		if !m.entries[idx].IsParent() {
			shown++
		}
	}
	if shown != 1 {
		t.Fatalf("filtered items = %d, want 1", shown)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEnter})
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc}) // clear filter
	m = press(m, runeKey("H"))
	found := false
	for _, idx := range m.visible {
		if m.entries[idx].Name == ".hidden" {
			found = true
		}
	}
	if !found {
		t.Fatal("H did not reveal hidden files")
	}
}

func TestSortAndReverse(t *testing.T) {
	m := loadScan(t, testModel(t))
	if m.sort != sortName || m.reverse {
		t.Fatalf("default sort = %v reverse=%v", m.sort, m.reverse)
	}
	m = press(m, runeKey("s"))
	if m.sort != sortSize {
		t.Fatalf("sort after s = %v", m.sort)
	}
	m = press(m, runeKey("S"))
	if !m.reverse {
		t.Fatal("S did not reverse")
	}
	m = press(m, runeKey("s"))
	m = press(m, runeKey("s"))
	m = press(m, runeKey("s"))
	if m.sort != sortName {
		t.Fatalf("sort did not wrap: %v", m.sort)
	}
}

func TestZipDialogDefaults(t *testing.T) {
	m := loadScan(t, testModel(t))
	// Put the cursor on archive.zip.
	for {
		e, ok := m.cur()
		if !ok {
			t.Fatal("archive.zip not found")
		}
		if e.Name == "archive.zip" {
			break
		}
		m = press(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	m = press(m, runeKey(" "))
	m = press(m, runeKey("z"))
	if m.overlay != ovZipPrompt {
		t.Fatalf("overlay = %v", m.overlay)
	}
	if m.zipDefault != "archive-archive.zip" {
		t.Fatalf("zip default = %q", m.zipDefault)
	}
	m = press(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay != ovNone {
		t.Fatal("esc did not close zip dialog")
	}
}

func TestStatusReportsAllFileSizes(t *testing.T) {
	m := loadScan(t, testModel(t))
	m.sizes[m.dir] = cachedSize{info: measure(m.dir)}
	got := m.folderSummary()
	if !strings.Contains(got, "16 B") {
		t.Errorf("folder summary = %q, want 16 B", got)
	}
}

func TestSelectionDoesNotDoubleCountNestedPaths(t *testing.T) {
	root := t.TempDir()
	folder := filepath.Join(root, "folder")
	child := filepath.Join(folder, "note.txt")
	writeTestFile(t, child, "x")

	m := NewModel(root, nil)
	m.addSelectedPath(child, false, 1)
	m.addSelectedPath(folder, true, 0)
	if len(m.selOrder) != 1 || !m.sel[folder] {
		t.Fatalf("parent did not supersede child: %v", m.selOrder)
	}

	m.clearSelection()
	m.addSelectedPath(folder, true, 0)
	m.addSelectedPath(child, false, 1)
	if len(m.selOrder) != 1 || !m.sel[folder] {
		t.Fatalf("child was selected under a folder: %v", m.selOrder)
	}
}
