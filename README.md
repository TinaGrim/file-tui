# file-tui

A quiet file manager for the terminal. Browse anything, inspect recursive
sizes, zip a selection, and delete safely — without leaving the keyboard.

Built with Bubble Tea and the same interaction model as `compress-tui`, but
scoped to files instead of media encoding.

## Features

- Browse every file type, not a media allow-list
- Recursive folder sizes that count **every** file, including hidden files
- Sort by name, size, date or kind; reverse at any time
- Live filter and hidden-file toggle
- Multi-select across folders
- Create `.zip` archives with progress, empty-folder support and a result summary
- Delete to Trash (recoverable) or permanently, behind a confirmation dialog
- Live selection total, folder total, and a detail pane with exact byte counts,
  permissions, dates and paths
- Responsive two-pane layout that collapses cleanly on narrow terminals

## Run

```sh
go build -o file-tui .
./file-tui                 # start in the current directory
./file-tui ~/Downloads     # start somewhere else
./file-tui notes.md        # open its folder, with notes.md preselected
```

Or install it directly:

```sh
go install github.com/TinaGrim/file-tui@latest
```

Requires Go 1.26.3 or newer to build. Zipping, measuring and deleting use only
the Go standard library; opening a file or folder uses the OS opener
(`open`, `explorer`, or `xdg-open`).

## Keys

| Key | Action |
| --- | --- |
| `↑` `↓` / `k` `j` | Move |
| `PgUp` `PgDn` | Page |
| `g` / `G` | First / last |
| `→` / `l` / `enter` | Open a folder, or open the file with the OS |
| `←` / `h` / `backspace` | Parent folder |
| `~` | Home folder |
| `space` | Select |
| `a` | Select everything visible |
| `x` | Deselect everything visible |
| `n` / `esc` | Clear selection |
| `/` | Filter by name |
| `s` | Cycle sort: name → size → date → kind |
| `S` | Reverse sort direction |
| `H` | Show / hide dotfiles |
| `d` | Delete — `t` moves to Trash, `D` deletes permanently |
| `z` | Create a zip from the selection (or the current item) |
| `o` | Open the current folder in Finder/Explorer/file manager |
| `ctrl+r` | Rescan and re-measure |
| `?` | Help |
| `q` | Quit (with a selection, use `Q` to force quit) |

## Notes

- Folder sizes are measured in the background and cached for five minutes.
  `ctrl+r` clears the cache and measures again.
- Zipping a folder stores the folder itself at the top of the archive. Hidden
  files and empty folders are included. Symbolic links are skipped and listed
  in the result notes.
- Trash is supported on macOS and Linux. Windows offers permanent delete only.
- Deletion refuses the filesystem root, the home folder, and any folder that
  contains the current working directory.

## Tests

```sh
go test ./...
```

The tests cover recursive all-file measurement, sorting, selection/filter
behavior, ZIP contents, both delete paths, and layout fit across terminal sizes.
