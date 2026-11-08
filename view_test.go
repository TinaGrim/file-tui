package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func sampleModel(t *testing.T) Model {
	t.Helper()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "document.pdf"), "1234567890")
	writeTestFile(t, filepath.Join(root, "archive.zip"), "12345")
	writeTestFile(t, filepath.Join(root, "日本語のとても長いファイル名.txt"), "wide")
	if err := os.MkdirAll(filepath.Join(root, "folder", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "folder", "note.txt"), "abc")
	m := NewModel(root, nil)
	m.ready = true
	list, err := listDir(root)
	if err != nil {
		t.Fatal(err)
	}
	m.entries = list
	m.reindex()
	m.sizes[m.dir] = cachedSize{info: measure(root), at: time.Now()}
	for _, e := range list {
		if e.Dir && !e.IsParent() {
			m.sizes[e.Path] = cachedSize{info: measure(e.Path), at: time.Now()}
		}
	}
	return m
}

func assertFits(t *testing.T, m Model, label string) {
	t.Helper()
	view := m.View()
	lines := strings.Split(view, "\n")
	if len(lines) > m.height {
		t.Errorf("%s: %d lines > height %d\n%s", label, len(lines), m.height, view)
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w > m.width {
			t.Errorf("%s: line %d width %d > %d: %q", label, i, w, m.width, line)
		}
	}
}

func TestViewsFitAtEveryWidth(t *testing.T) {
	base := sampleModel(t)
	for _, size := range [][2]int{{30, 12}, {48, 24}, {72, 24}, {88, 24}, {120, 30}, {160, 40}} {
		m := base
		m.width, m.height = size[0], size[1]
		assertFits(t, m, "browse")

		bad := m
		bad.status = "permission denied: this is a deliberately long status message that must never wrap the interface"
		bad.statusErr = true
		assertFits(t, bad, "long error status")

		del := m
		del.overlay = ovDelete
		del.deleteTargets = []string{filepath.Join(m.dir, "document.pdf")}
		assertFits(t, del, "delete overlay")

		zp := m
		zp.overlay = ovZipPrompt
		zp.zipRoots = []string{filepath.Join(m.dir, "document.pdf")}
		zp.zipDefault = "document.zip"
		assertFits(t, zp, "zip overlay")

		prog := m
		prog.screen = scrProgress
		prog.opKind = actZip
		prog.opTarget = filepath.Join(m.dir, "archive.zip")
		prog.opStarted = time.Now()
		prog.opProgress = opProgressMsg{scanning: true, current: "collecting files…"}
		assertFits(t, prog, "zip scanning")

		prog.opProgress = opProgressMsg{
			done: 5, total: 10, filesDone: 1, filesTotal: 2, current: "folder/note.txt",
		}
		assertFits(t, prog, "zip progress")

		delProg := m
		delProg.screen = scrProgress
		delProg.opKind = actDelete
		delProg.deleteTargets = []string{filepath.Join(m.dir, "folder")}
		delProg.opStarted = time.Now()
		delProg.opProgress = opProgressMsg{done: 1024, filesDone: 10, current: "folder/note.txt"}
		assertFits(t, delProg, "delete progress")

		sum := m
		sum.screen = scrSummary
		sum.result = opResult{
			action: actZip, roots: 1, files: 2, dirs: 3, bytes: 1024, outBytes: 512,
			path: filepath.Join(m.dir, "archive.zip"), elapsed: 2 * time.Second,
			problems: []string{"note.txt: permission denied"},
		}
		assertFits(t, sum, "zip summary")

		sum.result = opResult{
			action: actTrash, roots: 2, files: 10, dirs: 3, bytes: 2048,
			openDir: filepath.Join(homeDir(), ".Trash"), elapsed: time.Second,
		}
		assertFits(t, sum, "trash summary")

		sum.result = opResult{action: actZip, canceled: true, elapsed: time.Second}
		assertFits(t, sum, "canceled zip summary")

		sum.result = opResult{action: actDelete, canceled: true, roots: 1, files: 1, bytes: 10, elapsed: time.Second}
		assertFits(t, sum, "canceled delete summary")

		help := m
		help.screen = scrHelp
		assertFits(t, help, "help")
	}
}
