package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMeasureCountsEveryFileType(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, "notes.txt"), "abc")                 // 3
	writeTestFile(t, filepath.Join(root, "photo.jpg"), "12345")               // 5
	writeTestFile(t, filepath.Join(root, ".hidden"), "12")                    // 2
	writeTestFile(t, filepath.Join(root, "nested", "archive.zip"), "1234567") // 7

	got := measure(root)
	if !got.done || got.err != "" {
		t.Fatalf("measure failed: %+v", got)
	}
	if got.bytes != 17 {
		t.Errorf("bytes = %d, want 17 (all files, not just media)", got.bytes)
	}
	if got.files != 4 {
		t.Errorf("files = %d, want 4", got.files)
	}
	if got.dirs != 2 {
		t.Errorf("dirs = %d, want 2", got.dirs)
	}
}

func TestMeasureSingleFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.bin")
	writeTestFile(t, path, "hello")
	got := measure(path)
	if got.bytes != 5 || got.files != 1 || got.dirs != 0 || !got.done {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestMeasureContextCancels(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "a")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, canceled := measureContext(ctx, root); !canceled {
		t.Fatal("expected canceled measurement")
	}
}

func TestNormalizeRootsDropsNestedAndDuplicates(t *testing.T) {
	root := t.TempDir()
	left := filepath.Join(root, "left")
	right := filepath.Join(root, "right")
	nested := filepath.Join(left, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(right, 0o755); err != nil {
		t.Fatal(err)
	}
	got := normalizeRoots([]string{nested, left, right, left})
	if len(got) != 2 {
		t.Fatalf("got %v, want [%s %s]", got, left, right)
	}
	if got[0] != left || got[1] != right {
		t.Fatalf("got %v", got)
	}
}

func TestSortEntriesFoldersThenFiles(t *testing.T) {
	entries := []Entry{
		{Path: "/small.txt", Name: "small.txt", Size: 1},
		{Path: "/b", Name: "b", Dir: true},
		{Path: "/big.txt", Name: "big.txt", Size: 100},
		{Path: "/a", Name: "a", Dir: true},
		{Path: "/..", Name: "..", Dir: true},
	}
	dirSize := map[string]int64{"/a": 50, "/b": 10}
	sortEntries(entries, sortSize, false, func(e Entry) int64 {
		if e.Dir {
			return dirSize[e.Path]
		}
		return e.Size
	})
	if entries[0].Name != ".." || entries[1].Name != "a" || entries[2].Name != "b" {
		t.Fatalf("folder size ordering broken: %+v", entries)
	}
	if entries[3].Name != "big.txt" || entries[4].Name != "small.txt" {
		t.Fatalf("file size ordering broken: %+v", entries)
	}

	sortEntries(entries, sortName, true, func(e Entry) int64 { return e.Size })
	if entries[0].Name != ".." {
		t.Fatalf("parent not pinned: %+v", entries)
	}
}

func TestHumanAndExactSize(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		999:     "999 B",
		1024:    "1 KB",
		1536:    "1.5 KB",
		1048576: "1 MB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
	if got := exactBytes(1234567); got != "1,234,567 bytes" {
		t.Errorf("exactBytes = %q", got)
	}
}

func TestDefaultArchiveName(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "clip.mov")
	writeTestFile(t, file, "x")
	if got := defaultArchiveName(dir, []string{file}); got != "clip.zip" {
		t.Errorf("single file name = %q", got)
	}
	folder := filepath.Join(dir, "photos")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := defaultArchiveName(dir, []string{folder}); got != "photos.zip" {
		t.Errorf("single folder name = %q", got)
	}
	dotted := filepath.Join(dir, "photos.2026")
	if err := os.MkdirAll(dotted, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := defaultArchiveName(dir, []string{dotted}); got != "photos.2026.zip" {
		t.Errorf("dotted folder name = %q", got)
	}
	zipFile := filepath.Join(dir, "bundle.zip")
	writeTestFile(t, zipFile, "x")
	if got := defaultArchiveName(dir, []string{zipFile}); got != "bundle-archive.zip" {
		t.Errorf("zip source name = %q", got)
	}
}

func TestResolveArchiveTarget(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveArchiveTarget("backup", dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "backup.zip") {
		t.Errorf("got %q", got)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err = resolveArchiveTarget("sub", dir, "photos.zip")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(sub, "photos.zip") {
		t.Errorf("existing directory target = %q", got)
	}
}

func TestUniquePathAndName(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "a.zip"), "x")
	if got := filepath.Base(uniquePath(filepath.Join(dir, "a.zip"))); got != "a-2.zip" {
		t.Errorf("uniquePath = %q", got)
	}
	if got := uniqueNameIn(dir, "a.zip", false); got != "a 2.zip" {
		t.Errorf("uniqueNameIn file = %q", got)
	}
	if err := os.MkdirAll(filepath.Join(dir, "folder.name"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := uniqueNameIn(dir, "folder.name", true); got != "folder.name 2" {
		t.Errorf("uniqueNameIn dir = %q", got)
	}
}

func TestListDirIncludesEveryTypeAndHidden(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "a.txt"), "a")
	writeTestFile(t, filepath.Join(root, "b.jpg"), "b")
	writeTestFile(t, filepath.Join(root, ".dot"), "c")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	list, err := listDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var seen = map[string]bool{}
	for _, e := range list {
		seen[e.Name] = true
	}
	for _, name := range []string{"..", "a.txt", "b.jpg", ".dot", "sub"} {
		if !seen[name] {
			t.Errorf("missing %q in %+v", name, list)
		}
	}
	for _, e := range list {
		if e.Name == ".dot" && !e.Hidden {
			t.Errorf(".dot not marked hidden")
		}
	}
}

func TestGuardDeleteRefusesRoot(t *testing.T) {
	if err := guardDelete(string(filepath.Separator)); err == nil {
		t.Fatal("expected root refusal")
	}
	dir := t.TempDir()
	if err := guardDelete(dir); err != nil {
		t.Fatalf("temporary dir should be deletable: %v", err)
	}
}

func TestStatusSizesUseAllFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "document.pdf"), "1234567890") // 10
	writeTestFile(t, filepath.Join(root, "movie.mov"), "12345")         // 5
	m := NewModel(root, nil)
	m.width, m.height, m.ready = 120, 30, true
	list, err := listDir(root)
	if err != nil {
		t.Fatal(err)
	}
	m.entries = list
	m.reindex()
	m.sizes[m.dir] = cachedSize{info: measure(root), at: time.Now()}
	if !strings.Contains(m.folderSummary(), "15 B") {
		t.Errorf("folder summary = %q, want 15 B", m.folderSummary())
	}
}
