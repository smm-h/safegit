package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/stage"
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

// parseDiffOutput parses multi-file unified diff output into structured diffFile slices.
// It splits the raw output on "diff --git" boundaries and delegates single-file
// parsing to stage.ParseDiff to avoid duplicating hunk-parsing logic.
func parseDiffOutput(raw string) []diffFile {
	var files []diffFile

	// Split on "diff --git" boundaries. Each chunk (except possibly the first
	// empty one) is a single file's diff block.
	chunks := splitDiffChunks(raw)

	for _, chunk := range chunks {
		// Extract path info from the first line ("diff --git a/... b/...")
		firstNewline := strings.IndexByte(chunk, '\n')
		firstLine := chunk
		if firstNewline >= 0 {
			firstLine = chunk[:firstNewline]
		}

		df := diffFile{Hunks: []diffHunk{}}
		parts := strings.SplitN(firstLine, " ", 4)
		if len(parts) == 4 {
			df.Path = strings.TrimPrefix(parts[3], "b/")
			old := strings.TrimPrefix(parts[2], "a/")
			if old != df.Path {
				df.OldPath = old
			}
		}

		// Delegate hunk parsing to the shared implementation
		_, hunks := stage.ParseDiff(chunk)
		for _, h := range hunks {
			df.Hunks = append(df.Hunks, diffHunk{
				Header: h.Header,
				Lines:  h.Body,
			})
		}

		files = append(files, df)
	}
	return files
}

// splitDiffChunks splits multi-file diff output into per-file chunks.
// Each returned string starts with "diff --git".
func splitDiffChunks(raw string) []string {
	if len(raw) < 2 {
		return nil
	}
	const prefix = "diff --git "
	var chunks []string
	rest := raw
	for {
		// Find the next "diff --git" after the start
		idx := strings.Index(rest[1:], prefix)
		if idx < 0 {
			// No more boundaries; the rest is the last chunk
			chunks = append(chunks, rest)
			break
		}
		idx++ // adjust for the 1-char offset
		chunks = append(chunks, rest[:idx])
		rest = rest[idx:]
	}
	// Only return chunks that actually start with the prefix
	var result []string
	for _, c := range chunks {
		if strings.HasPrefix(c, prefix) {
			result = append(result, c)
		}
	}
	return result
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

	// Determine the revision to inspect (default HEAD).
	rev := "HEAD"
	if len(args) > 0 {
		// The first non-flag argument is the revision.
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				rev = a
				break
			}
		}
	}

	// Guard: JSON output only makes sense for commit objects.
	// Trees, blobs, and tags produce output that doesn't match our format.
	objType, _, typeErr := git.Run(ctx, "cat-file", "-t", rev)
	if typeErr != nil {
		emitJSON("show", nil, &jsonError{Code: 1, Message: fmt.Sprintf("cannot determine object type for %q", rev)}, nil)
		return 1
	}
	objType = strings.TrimSpace(objType)
	if objType != "commit" {
		emitJSON("show", nil, &jsonError{Code: 1, Message: "JSON output only supported for commit objects"}, nil)
		return 1
	}

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
