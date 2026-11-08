package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

var version = "1.0.0"

func main() {
	var (
		showVersion = flag.Bool("v", false, "print version and exit")
		dirFlag     = flag.String("d", "", "directory to open (default: current directory)")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("file-tui", version)
		return
	}

	start := expandHome(strings.TrimSpace(*dirFlag))
	explicitDir := start != ""
	var preselect []string
	args := flag.Args()

	if start == "" {
		if len(args) > 0 {
			start = expandHome(args[0])
			args = args[1:]
		} else if wd, err := os.Getwd(); err == nil {
			start = wd
		} else {
			start = homeDir()
		}
	}

	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	st, err := os.Stat(start)
	if err != nil {
		fmt.Fprintf(os.Stderr, "file-tui: %s: %v\n", start, err)
		os.Exit(1)
	}
	if !st.IsDir() {
		preselect = append(preselect, start)
		start = filepath.Dir(start)
	}

	// Extra file arguments are preselected. A directory argument is treated
	// only as a place to open, never as an implicit selection.
	for _, a := range args {
		a = expandHome(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if abs, err := filepath.Abs(a); err == nil {
			a = abs
		}
		if s, err := os.Stat(a); err == nil {
			if s.IsDir() {
				if !explicitDir && len(preselect) == 0 && a != start {
					start = a
				}
				continue
			}
			preselect = append(preselect, a)
		}
	}

	m := NewModel(start, preselect)
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "file-tui:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `%s — a quiet file manager for the terminal.

browse, inspect, zip and delete files without leaving the keyboard.
every size is recursive and covers every file, not a selected file type.

usage:
  file-tui [directory]
  file-tui file1.txt file2.zip

flags:
  -d dir    open this directory first
  -v        print version

keys:
  space select · enter open · d delete · z zip · / filter · ? help · q quit
`, version)
}
