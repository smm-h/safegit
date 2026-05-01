// Package wip manages work-in-progress refs and per-file locks.
// Wips are real commits on refs/safegit/wip/<wip-id>, surviving process death
// and not interfering with other agents.
package wip

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/smm-h/safegit/internal/git"
	"github.com/smm-h/safegit/internal/oplog"
)

// WipInfo describes an active wip snapshot.
type WipInfo struct {
	ID        string   `json:"id"`
	Files     []string `json:"files"`
	CreatedAt time.Time `json:"createdAt"`
	Ref       string   `json:"ref"`
}

// Create snapshots the listed files to a wip ref and reverts them in the working tree.
func Create(ctx context.Context, safegitDir string, files []string) (*WipInfo, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no files specified")
	}

	// Reject filenames with newlines (they break the commit message format)
	for _, f := range files {
		if strings.ContainsRune(f, '\n') {
			return nil, fmt.Errorf("filename %q contains a newline (unsupported)", f)
		}
	}

	// Validate none of the files are already wip-locked
	for _, f := range files {
		locked, wipID, err := IsLocked(safegitDir, f)
		if err != nil {
			return nil, fmt.Errorf("checking lock for %s: %w", f, err)
		}
		if locked {
			return nil, fmt.Errorf("file %s is already locked by wip %s", f, wipID)
		}
	}

	// Generate wip-id: 8 random hex chars
	id, err := generateID()
	if err != nil {
		return nil, fmt.Errorf("generating wip id: %w", err)
	}

	// Build a tree: seed tmp index from HEAD, add the listed files
	tmpIndexPath, cleanup, err := createTmpIndex(ctx, safegitDir)
	if err != nil {
		return nil, fmt.Errorf("creating tmp index: %w", err)
	}
	defer cleanup()

	// Add files (working tree content) into the tmp index
	for _, f := range files {
		env := []string{"GIT_INDEX_FILE=" + tmpIndexPath}
		_, _, err := git.RunWithEnv(ctx, env, "add", "--", f)
		if err != nil {
			return nil, fmt.Errorf("adding %s to index: %w", f, err)
		}
	}

	// Write tree
	treeSHA, err := git.WriteTree(ctx, tmpIndexPath)
	if err != nil {
		return nil, fmt.Errorf("write-tree: %w", err)
	}

	// Get HEAD for parent
	headSHA, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}

	// Create wip commit with structured message (one file: line per file
	// to handle filenames containing commas)
	var msgLines []string
	msgLines = append(msgLines, fmt.Sprintf("safegit wip %s", id))
	for _, f := range files {
		msgLines = append(msgLines, "file: "+f)
	}
	msg := strings.Join(msgLines, "\n")
	commitSHA, err := git.CommitTree(ctx, treeSHA, headSHA, msg)
	if err != nil {
		return nil, fmt.Errorf("commit-tree: %w", err)
	}

	// Create ref
	ref := "refs/safegit/wip/" + id
	if err := git.UpdateRef(ctx, ref, commitSHA, ""); err != nil {
		return nil, fmt.Errorf("creating ref %s: %w", ref, err)
	}

	// Write per-file lock files
	for _, f := range files {
		if err := writeLockFile(safegitDir, f, id); err != nil {
			return nil, fmt.Errorf("writing lock for %s: %w", f, err)
		}
	}

	// Note: we intentionally do NOT revert files in the working tree.
	// Reverting via "git checkout HEAD --" would destroy uncommitted edits
	// by other agents sharing the same worktree. The wip-lock prevents
	// commits to these files, which is the safety mechanism. The user can
	// manually revert if desired.

	// Append op log entry
	_ = oplog.Append(safegitDir, oplog.Entry{
		Op: "wip-create",
		Extra: map[string]interface{}{
			"id":    id,
			"files": files,
			"ref":   ref,
		},
	})

	return &WipInfo{
		ID:        id,
		Files:     files,
		CreatedAt: time.Now().UTC(),
		Ref:       ref,
	}, nil
}

// Restore applies a wip back to the working tree and deletes the ref.
func Restore(ctx context.Context, safegitDir, wipID string) ([]string, error) {
	ref := "refs/safegit/wip/" + wipID

	// Verify ref exists
	wipSHA, err := git.RevParse(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("wip %s not found (ref %s does not exist)", wipID, ref)
	}

	// Read file list from wip commit message
	files, err := parseFilesFromCommit(ctx, wipSHA)
	if err != nil {
		return nil, fmt.Errorf("parsing wip commit: %w", err)
	}

	// Restore files from the wip commit's tree to the working tree.
	// No clean-check needed: wip-locks prevent commits to these files,
	// so the only changes since wip-create are the user's own edits.
	checkoutArgs := append([]string{"checkout", wipSHA, "--"}, files...)
	_, _, err = git.Run(ctx, checkoutArgs...)
	if err != nil {
		return nil, fmt.Errorf("restoring wip files: %w", err)
	}

	// Delete per-file lock files
	for _, f := range files {
		removeLockFile(safegitDir, f)
	}

	// Delete wip ref
	_, _, err = git.Run(ctx, "update-ref", "-d", ref)
	if err != nil {
		return nil, fmt.Errorf("deleting ref %s: %w", ref, err)
	}

	// Append op log entry
	_ = oplog.Append(safegitDir, oplog.Entry{
		Op: "wip-restore",
		Extra: map[string]interface{}{
			"id":    wipID,
			"files": files,
		},
	})

	return files, nil
}

// List enumerates all active wips by scanning refs/safegit/wip/ refs.
func List(ctx context.Context, safegitDir string) ([]WipInfo, error) {
	out, _, err := git.Run(ctx, "for-each-ref", "--format=%(refname) %(objectname)", "refs/safegit/wip/")
	if err != nil {
		return nil, fmt.Errorf("listing wip refs: %w", err)
	}

	var wips []WipInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		ref := parts[0]
		sha := parts[1]

		// Extract ID from ref name
		id := strings.TrimPrefix(ref, "refs/safegit/wip/")

		// Parse files from commit message
		files, _ := parseFilesFromCommit(ctx, sha)

		// Get commit timestamp
		tsOut, _, _ := git.Run(ctx, "log", "-1", "--format=%aI", sha)
		createdAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(tsOut))

		wips = append(wips, WipInfo{
			ID:        id,
			Files:     files,
			CreatedAt: createdAt,
			Ref:       ref,
		})
	}

	return wips, nil
}

// IsLocked checks if a file is locked by an active wip.
// Returns (locked, wipID, err).
func IsLocked(safegitDir, filePath string) (bool, string, error) {
	lockPath := lockFilePath(safegitDir, filePath)
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("reading lock file: %w", err)
	}
	wipID := strings.TrimSpace(string(data))
	if wipID == "" {
		return false, "", nil
	}
	return true, wipID, nil
}

// OrphanLocks finds lock files whose wip ref no longer exists.
func OrphanLocks(ctx context.Context, safegitDir string) ([]string, error) {
	locksDir := filepath.Join(safegitDir, "wip-locks")
	entries, err := os.ReadDir(locksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading wip-locks dir: %w", err)
	}

	var orphans []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(locksDir, e.Name()))
		if err != nil {
			continue
		}
		wipID := strings.TrimSpace(string(data))
		if wipID == "" {
			orphans = append(orphans, e.Name())
			continue
		}
		// Check if ref still exists
		ref := "refs/safegit/wip/" + wipID
		_, err = git.RevParse(ctx, ref)
		if err != nil {
			orphans = append(orphans, e.Name())
		}
	}
	return orphans, nil
}

// CleanOrphanLocks removes lock files whose wip ref no longer exists.
func CleanOrphanLocks(ctx context.Context, safegitDir string) (int, error) {
	orphans, err := OrphanLocks(ctx, safegitDir)
	if err != nil {
		return 0, err
	}
	locksDir := filepath.Join(safegitDir, "wip-locks")
	removed := 0
	for _, name := range orphans {
		if err := os.Remove(filepath.Join(locksDir, name)); err == nil {
			removed++
		}
	}
	return removed, nil
}

// --- internal helpers ---

func generateID() (string, error) {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

// createTmpIndex seeds a temporary index from HEAD. Returns path, cleanup func, error.
func createTmpIndex(ctx context.Context, safegitDir string) (string, func(), error) {
	tmpDir := filepath.Join(safegitDir, "tmp")
	var rndBytes [4]byte
	if _, err := rand.Read(rndBytes[:]); err != nil {
		return "", nil, err
	}
	dirName := fmt.Sprintf("wip-%d-%s", os.Getpid(), hex.EncodeToString(rndBytes[:]))
	dir := filepath.Join(tmpDir, dirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", nil, err
	}
	indexPath := filepath.Join(dir, "index")
	if err := git.ReadTree(ctx, indexPath, "HEAD"); err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	return indexPath, cleanup, nil
}

// lockFilePath computes the lock file path for a given file.
// Path: <safegitDir>/wip-locks/<sha256(filepath)[:16]>
func lockFilePath(safegitDir, filePath string) string {
	h := sha256.Sum256([]byte(filePath))
	name := hex.EncodeToString(h[:])[:16]
	return filepath.Join(safegitDir, "wip-locks", name)
}

func writeLockFile(safegitDir, filePath, wipID string) error {
	lp := lockFilePath(safegitDir, filePath)
	return os.WriteFile(lp, []byte(wipID), 0644)
}

func removeLockFile(safegitDir, filePath string) {
	lp := lockFilePath(safegitDir, filePath)
	os.Remove(lp)
}

// parseFilesFromCommit reads file paths from a wip commit message.
// Supports both the new format ("file: " prefix per line) and the legacy
// format ("files: " with comma-separated list) for backward compatibility.
func parseFilesFromCommit(ctx context.Context, commitSHA string) ([]string, error) {
	out, _, err := git.Run(ctx, "log", "-1", "--format=%B", commitSHA)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "file: ") {
			files = append(files, strings.TrimPrefix(line, "file: "))
		}
	}
	if len(files) > 0 {
		return files, nil
	}
	// Legacy format: "files: a.txt, b.txt"
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "files: ") {
			raw := strings.TrimPrefix(line, "files: ")
			for _, p := range strings.Split(raw, ", ") {
				p = strings.TrimSpace(p)
				if p != "" {
					files = append(files, p)
				}
			}
			return files, nil
		}
	}
	return nil, fmt.Errorf("no file list found in commit %s", commitSHA)
}
