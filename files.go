package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Entry is one row in the browser. Directories keep their direct size at zero;
// their recursive size lives in the model's size cache.
type Entry struct {
	Path       string
	Name       string
	Dir        bool
	Link       bool
	LinkTarget string
	Hidden     bool
	Size       int64
	ModAt      time.Time
	Mode       fs.FileMode
}

// IsParent marks the synthetic ".." row. It can be opened but never selected,
// zipped or deleted.
func (e Entry) IsParent() bool { return e.Dir && e.Name == ".." }

type scanned struct {
	dir   string
	focus string
	index int
	list  []Entry
	err   error
}

// listDir reads every entry in a directory. Nothing is filtered by media type:
// this is a file manager, so archives, documents, binaries and links all show
// up. Hidden entries are returned too and filtered later by the UI, which
// makes toggling them instant.
func listDir(dir string) ([]Entry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	out := make([]Entry, 0, len(ents)+1)
	if parent := filepath.Dir(dir); parent != dir {
		out = append(out, Entry{
			Path: parent,
			Name: "..",
			Dir:  true,
			Mode: fs.ModeDir,
		})
	}

	for _, de := range ents {
		name := de.Name()
		full := filepath.Join(dir, name)
		if de.Type()&os.ModeSymlink != 0 {
			info, err := os.Lstat(full)
			if err != nil {
				continue
			}
			target, _ := os.Readlink(full)
			out = append(out, Entry{
				Path:       full,
				Name:       name,
				Link:       true,
				LinkTarget: target,
				Hidden:     strings.HasPrefix(name, "."),
				Size:       info.Size(),
				ModAt:      info.ModTime(),
				Mode:       info.Mode(),
			})
			continue
		}

		info, err := de.Info()
		if err != nil {
			continue
		}
		e := Entry{
			Path:   full,
			Name:   name,
			Dir:    de.IsDir(),
			Hidden: strings.HasPrefix(name, "."),
			Size:   info.Size(),
			ModAt:  info.ModTime(),
			Mode:   info.Mode(),
		}
		out = append(out, e)
	}
	return out, nil
}

type sortMode int

const (
	sortName sortMode = iota
	sortSize
	sortDate
	sortKind
)

func (s sortMode) String() string {
	switch s {
	case sortSize:
		return "size"
	case sortDate:
		return "date"
	case sortKind:
		return "kind"
	default:
		return "name"
	}
}

// descendingByDefault reports the natural direction of a sort. Names and kinds
// read best A→Z; sizes and dates read best big/new first.
func (s sortMode) descendingByDefault() bool {
	return s == sortSize || s == sortDate
}

// sortEntries keeps ".." pinned, then groups folders before files, then sorts
// within each group. Reverse flips the content order but never pushes folders
// below files, so navigation stays predictable.
func sortEntries(list []Entry, mode sortMode, reverse bool, sizeOf func(Entry) int64) {
	less := func(a, b Entry) bool {
		if a.IsParent() != b.IsParent() {
			return a.IsParent()
		}
		if a.Dir != b.Dir {
			return a.Dir
		}
		var c int
		switch mode {
		case sortSize:
			av, bv := sizeOf(a), sizeOf(b)
			if av != bv {
				if av > bv {
					c = -1
				} else {
					c = 1
				}
			}
		case sortDate:
			if !a.ModAt.Equal(b.ModAt) {
				if a.ModAt.After(b.ModAt) {
					c = -1
				} else {
					c = 1
				}
			}
		case sortKind:
			ak, bk := kindLabel(a), kindLabel(b)
			if ak != bk {
				c = strings.Compare(ak, bk)
			}
		}
		if c == 0 {
			c = strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if reverse {
			return c > 0
		}
		return c < 0
	}
	sort.SliceStable(list, func(i, j int) bool { return less(list[i], list[j]) })
}

// sizeInfo is the recursive measurement of one path. Every regular file and
// every symbolic link is counted, regardless of extension.
type sizeInfo struct {
	bytes int64
	files int
	dirs  int
	errs  int
	done  bool
	err   string
}

func measure(path string) sizeInfo {
	info, _ := measureContext(context.Background(), path)
	return info
}

// measureContext is the cancellable walk used by operations. The background
// browser cache uses measure, which never cancels.
func measureContext(ctx context.Context, path string) (sizeInfo, bool) {
	info, err := os.Lstat(path)
	if err != nil {
		return sizeInfo{done: true, err: err.Error()}, false
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return sizeInfo{bytes: info.Size(), files: 1, done: true}, false
	}
	if ctx.Err() != nil {
		return sizeInfo{}, true
	}

	out := sizeInfo{}
	canceled := false
	steps := 0
	walkErr := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		steps++
		if steps%512 == 0 && ctx.Err() != nil {
			canceled = true
			return filepath.SkipAll
		}
		if err != nil {
			out.errs++
			if p == path && out.err == "" {
				out.err = err.Error()
			}
			if d != nil && d.IsDir() && p != path {
				return filepath.SkipDir
			}
			return nil
		}
		if p == path {
			return nil
		}
		if d.IsDir() {
			out.dirs++
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			out.errs++
			return nil
		}
		out.bytes += fi.Size()
		out.files++
		return nil
	})
	if canceled {
		return out, true
	}
	if walkErr != nil {
		out.err = walkErr.Error()
	}
	out.done = true
	return out, false
}

// normalizeRoots dedupes paths and removes any path already covered by a
// selected ancestor. It keeps multi-folder selections safe: zipping a folder
// and a file inside it will not write the file twice.
func normalizeRoots(paths []string) []string {
	clean := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = filepath.Clean(p)
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		clean = append(clean, abs)
	}
	sort.Slice(clean, func(i, j int) bool {
		di, dj := strings.Count(clean[i], string(filepath.Separator)), strings.Count(clean[j], string(filepath.Separator))
		if di != dj {
			return di < dj
		}
		return clean[i] < clean[j]
	})
	out := make([]string, 0, len(clean))
	for _, p := range clean {
		covered := false
		for _, kept := range out {
			if isWithin(p, kept) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	return out
}

// isWithin reports whether child is inside parent (or equal to it).
func isWithin(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// guardDelete refuses the few paths that should never be removed by a stray
// keypress. The confirmation dialog is the first line of defence; this is the
// second, and it runs again inside the worker.
func guardDelete(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	if filepath.Dir(abs) == abs {
		return fmt.Errorf("refusing to delete the filesystem root")
	}
	home := filepath.Clean(homeDir())
	if abs == home {
		return fmt.Errorf("refusing to delete the home folder")
	}
	if wd, err := os.Getwd(); err == nil {
		wd = filepath.Clean(wd)
		if isWithin(wd, abs) {
			return fmt.Errorf("refusing to delete a folder that contains the current directory")
		}
	}
	return nil
}

func defaultArchiveName(dir string, roots []string) string {
	roots = normalizeRoots(roots)
	if len(roots) == 1 {
		base := filepath.Base(roots[0])
		isDir := false
		if st, err := os.Lstat(roots[0]); err == nil {
			isDir = st.IsDir()
		}
		if !isDir {
			if ext := filepath.Ext(base); ext != "" {
				base = strings.TrimSuffix(base, ext)
			}
		}
		name := base + ".zip"
		if filepath.Clean(filepath.Join(dir, name)) == filepath.Clean(roots[0]) {
			name = base + "-archive.zip"
		}
		return name
	}
	base := filepath.Base(dir)
	if base == "" || base == string(filepath.Separator) {
		base = "archive"
	}
	return base + "-" + time.Now().Format("20060102-150405") + ".zip"
}

// resolveArchiveTarget turns what the user typed into an absolute .zip path.
// Relative names live in dir; an existing directory means "put the archive
// inside it", using fallback as the archive name.
func resolveArchiveTarget(input, dir, fallback string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		input = fallback
	}
	if input == "" {
		input = defaultArchiveName(dir, nil)
	}
	input = expandHome(input)
	if !filepath.IsAbs(input) {
		input = filepath.Join(dir, input)
	}
	if st, err := os.Stat(input); err == nil && st.IsDir() {
		name := fallback
		if name == "" {
			name = defaultArchiveName(dir, nil)
		}
		input = filepath.Join(input, name)
	}
	if !strings.EqualFold(filepath.Ext(input), ".zip") {
		input += ".zip"
	}
	abs, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// uniquePath returns a path that does not exist yet, keeping the extension.
func uniquePath(path string) string {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return path
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	for i := 2; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s-%d%s", stem, i, ext))
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func expandHome(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return string(filepath.Separator)
	}
	return h
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	v := float64(n) / float64(div)
	s := strconv.FormatFloat(v, 'f', 1, 64)
	s = strings.TrimSuffix(s, ".0")
	return s + " " + string("KMGTPE"[exp]) + "B"
}

func exactBytes(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	out := b.String() + " bytes"
	if neg {
		return "-" + out
	}
	return out
}

func modLabel(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	now := time.Now()
	if t.Year() == now.Year() {
		return t.Format("Jan 02 15:04")
	}
	return t.Format("2006-01-02")
}

func modDetail(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04")
}

func permString(m fs.FileMode) string {
	const rwx = "rwxrwxrwx"
	b := []byte("----------")
	if m.IsDir() {
		b[0] = 'd'
	} else if m&os.ModeSymlink != 0 {
		b[0] = 'l'
	}
	for i := 0; i < 9; i++ {
		if m&(1<<uint(8-i)) != 0 {
			b[i+1] = rwx[i]
		}
	}
	return string(b)
}

// kindLabel is the human-facing type shown in the detail pane.
func kindLabel(e Entry) string {
	if e.Dir {
		return "folder"
	}
	if e.Link {
		return "symbolic link"
	}
	ext := strings.ToLower(filepath.Ext(e.Name))
	switch ext {
	case ".zip":
		return "zip archive"
	case ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar":
		return "archive"
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".heic", ".heif", ".avif", ".tif", ".tiff", ".bmp", ".svg":
		return "image"
	case ".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v", ".mpg", ".mpeg":
		return "video"
	case ".mp3", ".wav", ".flac", ".aac", ".m4a", ".ogg", ".opus":
		return "audio"
	case ".go", ".rs", ".py", ".js", ".ts", ".tsx", ".jsx", ".c", ".h", ".cc", ".cpp", ".java", ".rb", ".php", ".swift", ".kt", ".sh", ".zsh", ".fish":
		return "source"
	case ".md", ".txt", ".rtf", ".pdf", ".doc", ".docx", ".pages", ".tex":
		return "document"
	case ".json", ".yaml", ".yml", ".toml", ".ini", ".conf", ".xml", ".csv":
		return "data"
	case ".app", ".dmg", ".exe", ".msi", ".pkg", ".deb", ".rpm", ".apk":
		return "package"
	}
	if e.Mode&0o111 != 0 {
		return "executable"
	}
	return "file"
}

func typeSubtitle(e Entry) string {
	if e.Dir {
		return "folder"
	}
	if e.Link {
		return "symbolic link"
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(e.Name)), ".")
	label := kindLabel(e)
	if ext == "" {
		return label
	}
	return label + " · ." + ext
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func wrap(v, n int) int {
	if n <= 0 {
		return 0
	}
	v %= n
	if v < 0 {
		v += n
	}
	return v
}

func isTempArchive(path string) bool {
	base := filepath.Base(path)
	return strings.HasPrefix(base, ".file-tui-") && strings.HasSuffix(base, ".part")
}
