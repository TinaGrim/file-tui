package main

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuildAndWriteZipIncludesEverything(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "photos")
	writeTestFile(t, filepath.Join(src, "a.txt"), "hello")
	writeTestFile(t, filepath.Join(src, ".hidden"), "dot")
	writeTestFile(t, filepath.Join(src, "nested", "b.bin"), "123")
	if err := os.MkdirAll(filepath.Join(src, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "out.zip")

	plan, canceled, err := buildZipPlan(context.Background(), []string{src}, map[string]bool{target: true})
	if err != nil || canceled {
		t.Fatalf("plan: canceled=%v err=%v", canceled, err)
	}
	if plan.files != 3 {
		t.Errorf("plan files = %d, want 3", plan.files)
	}
	if plan.dirs != 3 {
		t.Errorf("plan dirs = %d, want 3", plan.dirs)
	}
	if plan.bytes != 11 {
		t.Errorf("plan bytes = %d, want 11", plan.bytes)
	}

	ch := make(chan opProgressMsg, 64)
	res := writeZip(context.Background(), plan, target, ch)
	if res.fatal != "" {
		t.Fatalf("writeZip: %s", res.fatal)
	}
	if res.outBytes == 0 {
		t.Fatal("empty archive")
	}
	if res.files != 3 || res.dirs != 3 {
		t.Errorf("result files/dirs = %d/%d", res.files, res.dirs)
	}
	if len(res.problems) != 0 {
		t.Errorf("unexpected problems: %v", res.problems)
	}

	zr, err := zip.OpenReader(target)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{
		"photos/", "photos/a.txt", "photos/.hidden",
		"photos/nested/", "photos/nested/b.bin", "photos/empty/",
	} {
		if !names[want] {
			t.Errorf("missing archive entry %q in %v", want, names)
		}
	}

	for _, f := range zr.File {
		if f.Name != "photos/a.txt" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(b) != "hello" {
			t.Errorf("content = %q", b)
		}
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, ".file-tui-*")); len(leftovers) > 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestZipPlanExcludesOutputArchive(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "a")
	target := filepath.Join(src, "self.zip")
	writeTestFile(t, target, "old archive")

	plan, _, err := buildZipPlan(context.Background(), []string{src}, map[string]bool{target: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range plan.entries {
		if strings.HasSuffix(e.name, "self.zip") {
			t.Fatalf("archive included itself: %+v", e)
		}
	}
}

func TestDeleteCommandPermanent(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone")
	writeTestFile(t, filepath.Join(gone, "a.txt"), "aa")
	writeTestFile(t, filepath.Join(gone, "sub", "b.txt"), "bbb")
	if err := os.MkdirAll(filepath.Join(gone, "sub", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "keep.txt")
	writeTestFile(t, keep, "keep")

	ch := make(chan opProgressMsg, 128)
	cmd := deleteCommand(context.Background(), []string{gone}, false, root, ch)
	msg := cmd()
	done, ok := msg.(operationDoneMsg)
	if !ok {
		t.Fatalf("got %T", msg)
	}
	res := done.res
	if res.fatal != "" || res.canceled {
		t.Fatalf("result: %+v", res)
	}
	if res.roots != 1 || res.files != 2 || res.dirs != 3 {
		t.Errorf("roots/files/dirs = %d/%d/%d, want 1/2/3", res.roots, res.files, res.dirs)
	}
	if len(res.completed) != 1 || res.completed[0] != gone {
		t.Errorf("completed = %v", res.completed)
	}
	if res.bytes != 5 {
		t.Errorf("bytes = %d, want 5", res.bytes)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Errorf("gone still exists: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("sibling was removed: %v", err)
	}
}

func TestDeleteCommandRefusesHome(t *testing.T) {
	ch := make(chan opProgressMsg, 16)
	cmd := deleteCommand(context.Background(), []string{homeDir()}, false, t.TempDir(), ch)
	msg := cmd()
	done, ok := msg.(operationDoneMsg)
	if !ok {
		t.Fatalf("got %T", msg)
	}
	if len(done.res.problems) == 0 || done.res.roots != 0 {
		t.Fatalf("home delete should be refused: %+v", done.res)
	}
}

func TestCopyPathCopiesTree(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeTestFile(t, filepath.Join(src, "a.txt"), "hello")
	writeTestFile(t, filepath.Join(src, "nested", "b.bin"), "world")
	dst := filepath.Join(root, "dst")
	if err := copyPath(context.Background(), src, dst); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"a.txt", "nested/b.bin"} {
		b, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Errorf("read %s: %v", rel, err)
			continue
		}
		want := "hello"
		if rel == "nested/b.bin" {
			want = "world"
		}
		if string(b) != want {
			t.Errorf("%s = %q, want %q", rel, b, want)
		}
	}
}

func TestTrashCommandMovesFile(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("trash is not available on this platform")
	}
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(fakeHome, ".local", "share"))
	trash := filepath.Join(fakeHome, ".Trash")
	if err := os.MkdirAll(trash, 0o755); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	file := filepath.Join(root, "doomed.txt")
	writeTestFile(t, file, "12345")

	ch := make(chan opProgressMsg, 128)
	cmd := deleteCommand(context.Background(), []string{file}, true, root, ch)
	msg := cmd()
	done, ok := msg.(operationDoneMsg)
	if !ok {
		t.Fatalf("got %T", msg)
	}
	res := done.res
	if len(res.problems) > 0 {
		t.Fatalf("problems: %v", res.problems)
	}
	if res.roots != 1 || res.files != 1 || res.bytes != 5 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.completed) != 1 || res.completed[0] != file {
		t.Errorf("completed = %v", res.completed)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("source still exists: %v", err)
	}
	dest := filepath.Join(trash, "doomed.txt")
	if runtime.GOOS == "linux" {
		dest = filepath.Join(fakeHome, ".local", "share", "Trash", "files", "doomed.txt")
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("file not in trash: %v", err)
	}
}
