package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/git"
)

// --- status ---

type statusEntry struct {
	Path     string `json:"path"`
	Status   string `json:"status"`             // "modified", "added", "deleted", "renamed", "copied", "untracked", "ignored"
	Staged   string `json:"staged"`             // X from XY (index status)
	Unstaged string `json:"unstaged"`           // Y from XY (worktree status)
	OrigPath string `json:"origPath,omitempty"` // for renames/copies
}

type statusData struct {
	Branch  string        `json:"branch"`
	SHA     string        `json:"sha"`
	Entries []statusEntry `json:"entries"`
}

// xyToStatus maps a single X or Y status character to a human-readable string.
func xyToStatus(c byte) string {
	switch c {
	case 'M':
		return "modified"
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case '?':
		return "untracked"
	case '!':
		return "ignored"
	case '.':
		return "unmodified"
	default:
		return string(c)
	}
}

// primaryStatus picks the most interesting status from XY for the top-level "status" field.
// Prefers the staged (X) status unless it's unmodified, in which case use unstaged (Y).
func primaryStatus(x, y byte) string {
	if x != '.' {
		return xyToStatus(x)
	}
	return xyToStatus(y)
}

func runStatus(flags globalFlags, args []string) int {
	if flags.format != formatJSON {
		return runPassthrough("status", args)
	}

	ctx := context.Background()
	stdout, stderr, err := git.Run(ctx, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		emitJSON("status", nil, &jsonError{Code: 1, Message: strings.TrimSpace(stderr)}, nil)
		return 1
	}

	data := statusData{Entries: []statusEntry{}}
	for _, line := range strings.Split(stdout, "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			data.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.oid "):
			data.SHA = strings.TrimPrefix(line, "# branch.oid ")

		case strings.HasPrefix(line, "1 "):
			// Ordinary changed entry:
			// 1 XY sub mH mI mW hH hI path
			fields := strings.SplitN(line, " ", 9)
			if len(fields) < 9 {
				continue
			}
			xy := fields[1]
			path := fields[8]
			data.Entries = append(data.Entries, statusEntry{
				Path:     path,
				Status:   primaryStatus(xy[0], xy[1]),
				Staged:   xyToStatus(xy[0]),
				Unstaged: xyToStatus(xy[1]),
			})

		case strings.HasPrefix(line, "2 "):
			// Rename/copy entry:
			// 2 XY sub mH mI mW hH hI Xscore path\torigPath
			fields := strings.SplitN(line, " ", 10)
			if len(fields) < 10 {
				continue
			}
			xy := fields[1]
			// The last field is "path\torigPath"
			pathPart := fields[9]
			parts := strings.SplitN(pathPart, "\t", 2)
			path := parts[0]
			origPath := ""
			if len(parts) == 2 {
				origPath = parts[1]
			}
			data.Entries = append(data.Entries, statusEntry{
				Path:     path,
				Status:   primaryStatus(xy[0], xy[1]),
				Staged:   xyToStatus(xy[0]),
				Unstaged: xyToStatus(xy[1]),
				OrigPath: origPath,
			})

		case strings.HasPrefix(line, "? "):
			path := strings.TrimPrefix(line, "? ")
			data.Entries = append(data.Entries, statusEntry{
				Path:     path,
				Status:   "untracked",
				Staged:   "untracked",
				Unstaged: "untracked",
			})

		case strings.HasPrefix(line, "! "):
			path := strings.TrimPrefix(line, "! ")
			data.Entries = append(data.Entries, statusEntry{
				Path:     path,
				Status:   "ignored",
				Staged:   "ignored",
				Unstaged: "ignored",
			})
		}
	}

	emitJSON("status", data, nil, nil)
	return 0
}

// --- diff ---

type diffFile struct {
	Path    string     `json:"path"`
	OldPath string     `json:"oldPath,omitempty"`
	Hunks   []diffHunk `json:"hunks"`
}

type diffHunk struct {
	Header string   `json:"header"` // the @@ line
	Lines  []string `json:"lines"`  // body lines
}

// parseDiffOutput parses unified diff output into structured diffFile slices.
func parseDiffOutput(raw string) []diffFile {
	var files []diffFile
	lines := strings.Split(raw, "\n")
	var cur *diffFile
	var curHunk *diffHunk

	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// Flush current hunk/file
			if curHunk != nil && cur != nil {
				cur.Hunks = append(cur.Hunks, *curHunk)
				curHunk = nil
			}
			if cur != nil {
				files = append(files, *cur)
			}
			cur = &diffFile{Hunks: []diffHunk{}}
			// Parse "diff --git a/path b/path"
			parts := strings.SplitN(line, " ", 4)
			if len(parts) == 4 {
				cur.Path = strings.TrimPrefix(parts[3], "b/")
				old := strings.TrimPrefix(parts[2], "a/")
				if old != cur.Path {
					cur.OldPath = old
				}
			}

		case strings.HasPrefix(line, "--- "):
			// skip; path already captured from diff --git line

		case strings.HasPrefix(line, "+++ "):
			// For renames or new files, the +++ line may be more accurate
			if cur != nil && cur.Path == "" {
				cur.Path = strings.TrimPrefix(line[4:], "b/")
			}

		case strings.HasPrefix(line, "@@ "):
			// New hunk
			if curHunk != nil && cur != nil {
				cur.Hunks = append(cur.Hunks, *curHunk)
			}
			curHunk = &diffHunk{Header: line}

		default:
			if curHunk != nil {
				curHunk.Lines = append(curHunk.Lines, line)
			}
		}
	}

	// Flush remaining
	if curHunk != nil && cur != nil {
		cur.Hunks = append(cur.Hunks, *curHunk)
	}
	if cur != nil {
		files = append(files, *cur)
	}
	return files
}

func runDiff(flags globalFlags, args []string) int {
	if flags.format != formatJSON {
		return runPassthrough("diff", args)
	}

	ctx := context.Background()
	gitArgs := append([]string{"diff", "--no-color", "--no-ext-diff"}, args...)
	stdout, stderr, err := git.Run(ctx, gitArgs...)
	if err != nil {
		emitJSON("diff", nil, &jsonError{Code: 1, Message: strings.TrimSpace(stderr)}, nil)
		return 1
	}

	files := parseDiffOutput(stdout)
	if files == nil {
		files = []diffFile{}
	}
	emitJSON("diff", map[string]interface{}{"files": files}, nil, nil)
	return 0
}

// --- log ---

type logEntry struct {
	SHA     string `json:"sha"`
	Author  string `json:"author"`
	Date    string `json:"date"`
	Message string `json:"message"`
}

func runLog(flags globalFlags, args []string) int {
	if flags.format != formatJSON {
		return runPassthrough("log", args)
	}

	ctx := context.Background()
	// Use null byte as record separator for safe multi-line message parsing.
	// %H = full SHA, %an <%ae> = author, %aI = ISO date, %B = full body
	gitArgs := append([]string{"log", "--format=%H%n%an <%ae>%n%aI%n%B%x00"}, args...)
	stdout, stderr, err := git.Run(ctx, gitArgs...)
	if err != nil {
		emitJSON("log", nil, &jsonError{Code: 1, Message: strings.TrimSpace(stderr)}, nil)
		return 1
	}

	var entries []logEntry
	// Split on null byte to separate records
	records := strings.Split(stdout, "\x00")
	for _, rec := range records {
		rec = strings.TrimSpace(rec)
		if rec == "" {
			continue
		}
		// Each record: SHA\nauthor\ndate\nmessage (possibly multi-line)
		lines := strings.SplitN(rec, "\n", 4)
		if len(lines) < 4 {
			continue
		}
		entries = append(entries, logEntry{
			SHA:     lines[0],
			Author:  lines[1],
			Date:    lines[2],
			Message: strings.TrimSpace(lines[3]),
		})
	}
	if entries == nil {
		entries = []logEntry{}
	}

	emitJSON("log", map[string]interface{}{"entries": entries}, nil, nil)
	return 0
}

// --- show ---

type showResult struct {
	SHA     string     `json:"sha"`
	Author  string     `json:"author"`
	Date    string     `json:"date"`
	Message string     `json:"message"`
	Files   []diffFile `json:"files,omitempty"`
}

func runShow(flags globalFlags, args []string) int {
	if flags.format != formatJSON {
		return runPassthrough("show", args)
	}

	ctx := context.Background()

	// Use a unique delimiter to split commit metadata from diff output.
	// The %x00 after %B marks end of metadata section.
	gitArgs := append([]string{"show", "--format=%H%n%an <%ae>%n%aI%n%B%x00", "--no-color"}, args...)
	stdout, stderr, err := git.Run(ctx, gitArgs...)
	if err != nil {
		emitJSON("show", nil, &jsonError{Code: 1, Message: strings.TrimSpace(stderr)}, nil)
		return 1
	}

	// Split at null byte: metadata before, diff after
	parts := strings.SplitN(stdout, "\x00", 2)
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		emitJSON("show", nil, &jsonError{Code: 1, Message: "unexpected empty output from git show"}, nil)
		return 1
	}

	meta := strings.TrimSpace(parts[0])
	metaLines := strings.SplitN(meta, "\n", 4)
	if len(metaLines) < 4 {
		// Might be a non-commit object; fall through with what we have
		fmt.Fprint(os.Stderr, stdout)
		return 0
	}

	result := showResult{
		SHA:     metaLines[0],
		Author:  metaLines[1],
		Date:    metaLines[2],
		Message: strings.TrimSpace(metaLines[3]),
	}

	// Parse diff portion if present
	if len(parts) == 2 {
		diffPart := strings.TrimSpace(parts[1])
		if diffPart != "" {
			result.Files = parseDiffOutput(diffPart)
		}
	}

	emitJSON("show", result, nil, nil)
	return 0
}
