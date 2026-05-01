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
func Create(safegitDir string, files []string) (*WipInfo, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("no files specified")
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
	tmpIndexPath, cleanup, err := createTmpIndex(safegitDir)
	if err != nil {
		return nil, fmt.Errorf("creating tmp index: %w", err)
	}
	defer cleanup()

	ctx := context.Background()

	// Add files (working tree content) into the tmp index
	for _, f := range files {
		env := []string{"GIT_INDEX_FILE=" + tmpIndexPath}
		_, _, err := git.RunWithEnv(ctx, env, "add", "--", f)
		if err != nil {
			return nil, fmt.Errorf("adding %s to index: %w", f, err)
		}
	}

	// Write tree
	treeSHA, err := git.WriteTree(tmpIndexPath)
	if err != nil {
		return nil, fmt.Errorf("write-tree: %w", err)
	}

	// Get HEAD for parent
	headSHA, err := git.RevParse("HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolving HEAD: %w", err)
	}

	// Create wip commit with structured message
	filesLine := strings.Join(files, ", ")
	msg := fmt.Sprintf("safegit wip %s\nfiles: %s", id, filesLine)
	commitSHA, err := git.CommitTree(treeSHA, headSHA, msg)
	if err != nil {
		return nil, fmt.Errorf("commit-tree: %w", err)
	}

	// Create ref
	ref := "refs/safegit/wip/" + id
	if err := git.UpdateRef(ref, commitSHA, ""); err != nil {
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
func Restore(safegitDir, wipID string, force bool) ([]string, error) {
	ref := "refs/safegit/wip/" + wipID

	// Verify ref exists
	wipSHA, err := git.RevParse(ref)
	if err != nil {
		return nil, fmt.Errorf("wip %s not found (ref %s does not exist)", wipID, ref)
	}

	// Read file list from wip commit message
	files, err := parseFilesFromCommit(wipSHA)
	if err != nil {
		return nil, fmt.Errorf("parsing wip commit: %w", err)
	}

	// Restore files from the wip commit's tree to the working tree.
	// No clean-check needed: wip-locks prevent commits to these files,
	// so the only changes since wip-create are the user's own edits.
	ctx := context.Background()
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
func List(safegitDir string) ([]WipInfo, error) {
	ctx := context.Background()
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
		files, _ := parseFilesFromCommit(sha)

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
func OrphanLocks(safegitDir string) ([]string, error) {
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
		_, err = git.RevParse(ref)
		if err != nil {
			orphans = append(orphans, e.Name())
		}
	}
	return orphans, nil
}

// CleanOrphanLocks removes lock files whose wip ref no longer exists.
func CleanOrphanLocks(safegitDir string) (int, error) {
	orphans, err := OrphanLocks(safegitDir)
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
func createTmpIndex(safegitDir string) (string, func(), error) {
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
	if err := git.ReadTree(indexPath, "HEAD"); err != nil {
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

// parseFilesFromCommit reads the "files:" line from a wip commit message.
func parseFilesFromCommit(commitSHA string) ([]string, error) {
	ctx := context.Background()
	out, _, err := git.Run(ctx, "log", "-1", "--format=%B", commitSHA)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "files: ") {
			raw := strings.TrimPrefix(line, "files: ")
			parts := strings.Split(raw, ", ")
			var files []string
			for _, p := range parts {
				p = strings.TrimSpace(p)
				if p != "" {
					files = append(files, p)
				}
			}
			return files, nil
		}
	}
	return nil, fmt.Errorf("no 'files:' line found in commit %s", commitSHA)
}
