package runner

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// maxScaffoldBytes caps any single scaffold read.
const maxScaffoldBytes = 1 << 20

// Scaffold files (PROGRESS.md, QUESTION.md, .spec-complete, REVIEW.md) live
// in the clone, which an untrusted agent fully controls. Every helper below uses
// Lstat plus O_NOFOLLOW so a hostile symlink there can never make the
// runner read or write an arbitrary host path; it only ever touches the
// link itself, and logs a SECURITY line when it does.

func (r *run) scaffoldExists(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if !fi.Mode().IsRegular() {
		r.logf("SECURITY: %s is not a regular file (%s) — ignoring it", filepath.Base(path), fi.Mode())
		return false
	}
	return true
}

func (r *run) scaffoldRead(path string) string {
	data, err := ReadScaffold(path)
	if err != nil {
		if !os.IsNotExist(err) {
			r.logf("SECURITY: %v", err)
		}
		return ""
	}
	return data
}

// ReadScaffold reads a scaffold file (PROGRESS.md, QUESTION.md, ...) using
// the same Lstat + O_NOFOLLOW rules as the run's own scaffold helpers: a
// symlink there — planted by an untrusted agent — is rejected rather than
// followed. Exported so `nightshift review` can read PROGRESS.md from a
// run's clone without a *run receiver.
func ReadScaffold(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file (%s) — refusing to read it", filepath.Base(path), fi.Mode())
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("refusing to follow %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxScaffoldBytes))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (r *run) scaffoldWrite(path, content string) {
	fi, err := os.Lstat(path)
	if err == nil && !fi.Mode().IsRegular() {
		r.logf("SECURITY: %s is not a regular file (%s) — refusing to overwrite it", filepath.Base(path), fi.Mode())
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		r.logf("SECURITY: refusing to write %s: %v", path, err)
		return
	}
	defer f.Close()
	_, _ = io.WriteString(f, content)
}

func (r *run) scaffoldRemove(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if !fi.Mode().IsRegular() {
		r.logf("SECURITY: %s is not a regular file (%s) — removing the link, not following it", filepath.Base(path), fi.Mode())
	}
	return os.Remove(path) == nil
}

func (r *run) scaffoldAppend(path, text string) {
	fi, err := os.Lstat(path)
	if err == nil && !fi.Mode().IsRegular() {
		r.logf("SECURITY: %s is not a regular file (%s) — refusing to append", filepath.Base(path), fi.Mode())
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		r.logf("SECURITY: refusing to append to %s: %v", path, err)
		return
	}
	defer f.Close()
	fmt.Fprint(f, text)
}
