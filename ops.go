package main

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type actionKind int

const (
	actNone actionKind = iota
	actZip
	actTrash
	actDelete
)

func (a actionKind) progressTitle() string {
	switch a {
	case actZip:
		return "zipping"
	case actTrash:
		return "moving to trash"
	case actDelete:
		return "deleting"
	}
	return ""
}

func (a actionKind) doneTitle(canceled bool) string {
	if canceled {
		switch a {
		case actZip:
			return "Zip canceled"
		case actTrash:
			return "Trash canceled"
		case actDelete:
			return "Delete canceled"
		}
	}
	switch a {
	case actZip:
		return "Archive created"
	case actTrash:
		return "Moved to Trash"
	case actDelete:
		return "Deleted"
	}
	return "Done"
}

// opResult is the compact record shown on the result screen. bytes is the
// amount read (zip) or removed (delete); outBytes is the archive size.
type opResult struct {
	action    actionKind
	roots     int
	files     int
	dirs      int
	bytes     int64
	outBytes  int64
	path      string
	openDir   string
	problems  []string
	completed []string
	canceled  bool
	fatal     string
	elapsed   time.Duration
}

func (r *opResult) addProblem(what string, err error) {
	if err == nil || len(r.problems) >= 8 {
		return
	}
	msg := what + ": " + err.Error()
	for _, p := range r.problems {
		if p == msg {
			return
		}
	}
	r.problems = append(r.problems, msg)
}

type opProgressMsg struct {
	scanning   bool
	done       int64
	total      int64
	filesDone  int
	filesTotal int
	current    string
}

type operationDoneMsg struct{ res opResult }

func waitProgress(ch chan opProgressMsg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func sendProgress(ch chan opProgressMsg, msg opProgressMsg) {
	select {
	case ch <- msg:
	default:
	}
}

// ---------------------------------------------------------------------- zip

type zipEntry struct {
	src  string
	name string
	dir  bool
	mode fs.FileMode
	mod  time.Time
}

type zipPlan struct {
	entries  []zipEntry
	bytes    int64
	files    int
	dirs     int
	problems []string
}

// buildZipPlan resolves archive names before any bytes are written. The
// exclusion map keeps an archive from swallowing a previous copy of itself.
func buildZipPlan(ctx context.Context, roots []string, exclude map[string]bool) (zipPlan, bool, error) {
	roots = normalizeRoots(roots)
	if len(roots) == 0 {
		return zipPlan{}, false, fmt.Errorf("nothing to zip")
	}

	var plan zipPlan
	used := map[string]bool{}
	steps := 0

	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return plan, true, nil
		}
		info, err := os.Lstat(root)
		if err != nil {
			plan.problems = append(plan.problems, filepath.Base(root)+": "+err.Error())
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			plan.problems = append(plan.problems, filepath.Base(root)+": symbolic link skipped")
			continue
		}

		base := filepath.Base(root)
		if !info.IsDir() {
			if !info.Mode().IsRegular() {
				plan.problems = append(plan.problems, base+": not a regular file, skipped")
				continue
			}
			name := uniqueArchiveName(base, used, false)
			plan.entries = append(plan.entries, zipEntry{
				src: root, name: name, mode: info.Mode(), mod: info.ModTime(),
			})
			plan.bytes += info.Size()
			plan.files++
			continue
		}

		rootName := uniqueArchiveName(base, used, true)
		plan.entries = append(plan.entries, zipEntry{
			src: root, name: rootName + "/", dir: true, mode: info.Mode(), mod: info.ModTime(),
		})
		plan.dirs++

		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				if len(plan.problems) < 8 {
					plan.problems = append(plan.problems, filepath.Base(p)+": "+walkErr.Error())
				}
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if p == root {
				return nil
			}
			steps++
			if steps%512 == 0 && ctx.Err() != nil {
				return ctx.Err()
			}
			abs, err := filepath.Abs(p)
			if err != nil {
				abs = p
			}
			if exclude[abs] || isTempArchive(abs) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			name := rootName + "/" + filepath.ToSlash(rel)
			if d.IsDir() {
				fi, err := d.Info()
				if err != nil {
					return nil
				}
				plan.entries = append(plan.entries, zipEntry{
					src: p, name: name + "/", dir: true, mode: fi.Mode(), mod: fi.ModTime(),
				})
				plan.dirs++
				return nil
			}
			if d.Type()&os.ModeSymlink != 0 {
				if len(plan.problems) < 8 {
					plan.problems = append(plan.problems, filepath.Base(p)+": symbolic link skipped")
				}
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				if len(plan.problems) < 8 {
					plan.problems = append(plan.problems, filepath.Base(p)+": "+err.Error())
				}
				return nil
			}
			if !fi.Mode().IsRegular() {
				return nil
			}
			plan.entries = append(plan.entries, zipEntry{
				src: p, name: name, mode: fi.Mode(), mod: fi.ModTime(),
			})
			plan.bytes += fi.Size()
			plan.files++
			return nil
		})
		if err != nil {
			if ctx.Err() != nil {
				return plan, true, nil
			}
			plan.problems = append(plan.problems, filepath.Base(root)+": "+err.Error())
		}
	}
	if len(plan.entries) == 0 {
		msg := "nothing to zip"
		if len(plan.problems) > 0 {
			msg += " — " + plan.problems[0]
		}
		return plan, false, fmt.Errorf("%s", msg)
	}
	if plan.files == 0 && plan.dirs == 0 {
		return plan, false, fmt.Errorf("nothing to zip")
	}
	return plan, false, nil
}

func uniqueArchiveName(base string, used map[string]bool, dir bool) string {
	base = strings.TrimSuffix(base, "/")
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "archive"
	}
	if !used[base] {
		used[base] = true
		return base
	}
	stem, ext := base, ""
	if !dir {
		ext = filepath.Ext(base)
		stem = strings.TrimSuffix(base, ext)
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if !used[candidate] {
			used[candidate] = true
			return candidate
		}
	}
}

func zipCommand(ctx context.Context, roots []string, target string, ch chan opProgressMsg) tea.Cmd {
	return func() tea.Msg {
		defer close(ch)
		start := time.Now()
		sendProgress(ch, opProgressMsg{scanning: true, current: "collecting files…"})

		exclude := map[string]bool{target: true}
		plan, canceled, err := buildZipPlan(ctx, roots, exclude)
		if canceled {
			return operationDoneMsg{res: opResult{
				action: actZip, canceled: true, elapsed: time.Since(start),
			}}
		}
		if err != nil {
			return operationDoneMsg{res: opResult{
				action: actZip, fatal: err.Error(), path: target,
				openDir: filepath.Dir(target), elapsed: time.Since(start),
			}}
		}

		sendProgress(ch, opProgressMsg{
			total: plan.bytes, filesTotal: plan.files,
			current: "writing archive…",
		})

		res := writeZip(ctx, plan, target, ch)
		res.action = actZip
		res.path = target
		res.openDir = filepath.Dir(target)
		res.roots = len(normalizeRoots(roots))
		res.bytes = plan.bytes
		res.files = plan.files
		res.dirs = plan.dirs
		res.elapsed = time.Since(start)
		res.problems = append(plan.problems, res.problems...)
		if res.canceled {
			res.path = ""
			res.openDir = ""
			res.files = 0
			res.dirs = 0
			res.bytes = 0
			res.outBytes = 0
		}
		return operationDoneMsg{res: res}
	}
}

func writeZip(ctx context.Context, plan zipPlan, target string, ch chan opProgressMsg) opResult {
	var res opResult

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		res.fatal = err.Error()
		return res
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".file-tui-*.part")
	if err != nil {
		res.fatal = err.Error()
		return res
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	zw := zip.NewWriter(tmp)
	var done int64
	filesDone := 0
	lastEmit := time.Time{}
	emit := func(current string) {
		now := time.Now()
		if now.Sub(lastEmit) < 90*time.Millisecond {
			return
		}
		lastEmit = now
		sendProgress(ch, opProgressMsg{
			done: done, total: plan.bytes, filesDone: filesDone,
			filesTotal: plan.files, current: current,
		})
	}

	abort := func(err error) opResult {
		zw.Close()
		cleanup()
		res.fatal = err.Error()
		res.elapsed = 0
		return res
	}

	for _, e := range plan.entries {
		if err := ctx.Err(); err != nil {
			zw.Close()
			cleanup()
			res.canceled = true
			return res
		}

		header := &zip.FileHeader{
			Name:     e.name,
			Method:   zip.Deflate,
			Modified: e.mod,
		}
		if e.dir {
			header.Method = zip.Store
			header.SetMode(e.mode | os.ModeDir)
			if _, err := zw.CreateHeader(header); err != nil {
				return abort(err)
			}
			continue
		}
		if !e.mode.IsRegular() {
			continue
		}

		// Open before creating the entry so an unreadable file leaves no
		// misleading empty placeholder inside the archive.
		src, err := os.Open(e.src)
		if err != nil {
			res.addProblem(filepath.Base(e.src), err)
			continue
		}
		header.SetMode(e.mode.Perm())
		w, err := zw.CreateHeader(header)
		if err != nil {
			src.Close()
			return abort(err)
		}
		buf := make([]byte, 256*1024)
		for {
			if err := ctx.Err(); err != nil {
				src.Close()
				zw.Close()
				cleanup()
				res.canceled = true
				return res
			}
			n, readErr := src.Read(buf)
			if n > 0 {
				if _, err := w.Write(buf[:n]); err != nil {
					src.Close()
					return abort(err)
				}
				done += int64(n)
				emit(e.name)
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				src.Close()
				return abort(readErr)
			}
		}
		src.Close()
		filesDone++
		emit(e.name)
	}

	if err := zw.Close(); err != nil {
		cleanup()
		res.fatal = err.Error()
		return res
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		res.fatal = err.Error()
		return res
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		res.fatal = err.Error()
		return res
	}

	out, err := os.Stat(tmpName)
	if err != nil {
		os.Remove(tmpName)
		res.fatal = err.Error()
		return res
	}
	if out.Size() == 0 {
		os.Remove(tmpName)
		res.fatal = "archive is empty"
		return res
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		res.fatal = err.Error()
		return res
	}

	res.bytes = plan.bytes
	res.outBytes = out.Size()
	res.files = plan.files
	res.dirs = plan.dirs
	sendProgress(ch, opProgressMsg{
		done: plan.bytes, total: plan.bytes, filesDone: plan.files,
		filesTotal: plan.files, current: "done",
	})
	return res
}

// -------------------------------------------------------------------- trash

func trashSupported() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "linux"
}

func trashRoot() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir(), ".Trash")
	case "linux":
		data := os.Getenv("XDG_DATA_HOME")
		if data == "" {
			data = filepath.Join(homeDir(), ".local", "share")
		}
		return filepath.Join(data, "Trash", "files")
	default:
		return ""
	}
}

func moveToTrash(ctx context.Context, path string) error {
	switch runtime.GOOS {
	case "darwin":
		dest := filepath.Join(trashRoot(), filepath.Base(path))
		dest = uniqueDest(dest, path)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		return movePath(ctx, path, dest)
	case "linux":
		return linuxTrash(ctx, path)
	default:
		return fmt.Errorf("trash is not supported on %s; use permanent delete", runtime.GOOS)
	}
}

func linuxTrash(ctx context.Context, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return err
	}
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		data = filepath.Join(homeDir(), ".local", "share")
	}
	base := filepath.Join(data, "Trash")
	filesDir := filepath.Join(base, "files")
	infoDir := filepath.Join(base, "info")
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(infoDir, 0o755); err != nil {
		return err
	}

	destName := uniqueNameIn(filesDir, filepath.Base(abs), st.IsDir())
	dest := filepath.Join(filesDir, destName)
	infoPath := filepath.Join(infoDir, destName+".trashinfo")
	info := fmt.Sprintf("[Trash Info]\nPath=%s\nDeletionDate=%s\n",
		(&url.URL{Path: abs}).EscapedPath(),
		time.Now().Format("2006-01-02T15:04:05"))
	if err := os.WriteFile(infoPath, []byte(info), 0o644); err != nil {
		return err
	}
	if err := movePath(ctx, abs, dest); err != nil {
		os.Remove(infoPath)
		return err
	}
	return nil
}

// movePath first tries an atomic rename (the common same-volume case) and
// falls back to a copy-and-remove for files on another volume.
func movePath(ctx context.Context, src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	if err := copyPath(ctx, src, dst); err != nil {
		os.RemoveAll(dst)
		return err
	}
	if err := os.RemoveAll(src); err != nil {
		return fmt.Errorf("copied to trash but could not remove original: %w", err)
	}
	return nil
}

func copyPath(ctx context.Context, src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, de := range ents {
			if err := copyPath(ctx, filepath.Join(src, de.Name()), filepath.Join(dst, de.Name())); err != nil {
				return err
			}
		}
		return os.Chtimes(dst, info.ModTime(), info.ModTime())
	default:
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		buf := make([]byte, 256*1024)
		for {
			if err := ctx.Err(); err != nil {
				out.Close()
				return err
			}
			n, readErr := in.Read(buf)
			if n > 0 {
				if _, err := out.Write(buf[:n]); err != nil {
					out.Close()
					return err
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				out.Close()
				return readErr
			}
		}
		if err := out.Close(); err != nil {
			return err
		}
		return os.Chtimes(dst, info.ModTime(), info.ModTime())
	}
}

func uniqueDest(dest, src string) string {
	info, err := os.Lstat(src)
	isDir := err == nil && info.IsDir()
	return filepath.Join(filepath.Dir(dest), uniqueNameIn(filepath.Dir(dest), filepath.Base(dest), isDir))
}

func uniqueNameIn(dir, name string, isDir bool) string {
	if _, err := os.Lstat(filepath.Join(dir, name)); os.IsNotExist(err) {
		return name
	}
	ext := ""
	stem := name
	if !isDir {
		if dot := strings.LastIndex(name, "."); dot > 0 {
			ext = name[dot:]
			stem = name[:dot]
		}
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s %d%s", stem, i, ext)
		if _, err := os.Lstat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate
		}
	}
}

// ------------------------------------------------------------------- delete

func deleteCommand(ctx context.Context, roots []string, trash bool, openDir string, ch chan opProgressMsg) tea.Cmd {
	return func() tea.Msg {
		defer close(ch)
		start := time.Now()
		var res opResult

		if trash {
			res.action = actTrash
			res.openDir = trashRoot()
			sendProgress(ch, opProgressMsg{scanning: true, current: "measuring…"})
			roots := normalizeRoots(roots)
			type measuredRoot struct {
				path string
				info sizeInfo
				dir  bool
			}
			var eligible []measuredRoot
			var seenBytes int64
			var seenFiles int
			// Measure first so the confirmation number and the summary agree,
			// and so the move phase can report one clean total.
			for _, root := range roots {
				if ctx.Err() != nil {
					res.canceled = true
					break
				}
				if err := guardDelete(root); err != nil {
					res.addProblem(filepath.Base(root), err)
					continue
				}
				info, wasCanceled := measureContext(ctx, root)
				if wasCanceled {
					res.canceled = true
					break
				}
				st, err := os.Lstat(root)
				if err != nil {
					res.addProblem(filepath.Base(root), err)
					continue
				}
				eligible = append(eligible, measuredRoot{path: root, info: info, dir: st.IsDir()})
				seenBytes += info.bytes
				seenFiles += info.files
				sendProgress(ch, opProgressMsg{scanning: true, done: seenBytes, filesDone: seenFiles, current: filepath.Base(root)})
			}
			if !res.canceled {
				for _, mr := range eligible {
					if ctx.Err() != nil {
						res.canceled = true
						break
					}
					sendProgress(ch, opProgressMsg{scanning: false, done: seenBytes, filesDone: seenFiles, current: filepath.Base(mr.path)})
					if err := moveToTrash(ctx, mr.path); err != nil {
						if ctx.Err() != nil {
							res.canceled = true
							break
						}
						res.addProblem(filepath.Base(mr.path), err)
						continue
					}
					res.roots++
					res.bytes += mr.info.bytes
					res.files += mr.info.files
					res.dirs += mr.info.dirs
					if mr.dir {
						res.dirs++
					}
					res.completed = append(res.completed, mr.path)
				}
			}
		} else {
			res.action = actDelete
			res.openDir = openDir
			sendProgress(ch, opProgressMsg{current: "deleting…"})
			lastEmit := time.Time{}
			for _, root := range normalizeRoots(roots) {
				if ctx.Err() != nil {
					res.canceled = true
					break
				}
				if err := guardDelete(root); err != nil {
					res.addProblem(filepath.Base(root), err)
					continue
				}
				err := removeTree(ctx, root, &res, func(current string) {
					if time.Since(lastEmit) < 90*time.Millisecond {
						return
					}
					lastEmit = time.Now()
					sendProgress(ch, opProgressMsg{
						done: res.bytes, filesDone: res.files, current: current,
					})
				})
				if err != nil {
					if ctx.Err() != nil {
						res.canceled = true
						break
					}
					res.addProblem(filepath.Base(root), err)
					continue
				}
				res.roots++
				res.completed = append(res.completed, root)
			}
		}
		res.elapsed = time.Since(start)
		return operationDoneMsg{res: res}
	}
}

// removeTree deletes bottom-up so a canceled run still leaves a coherent tree
// and reports the bytes it actually freed.
func removeTree(ctx context.Context, path string, res *opResult, tick func(string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		ents, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, de := range ents {
			if err := removeTree(ctx, filepath.Join(path, de.Name()), res, tick); err != nil {
				return err
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		res.dirs++
		tick(filepath.Base(path))
		return nil
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	res.bytes += info.Size()
	res.files++
	tick(filepath.Base(path))
	return nil
}
