package k8sbackend

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"pentagi/pkg/executor"
)

const maxListEntries = 10000

// StatPath stats one path via `stat` (k8s has no archive-stat API). Mode is the
// permission bits; Mtime is from the file's epoch mtime.
func (b *Backend) StatPath(ctx context.Context, id, p string) (executor.PathStat, error) {
	out, _, code, err := b.execCapture(ctx, id, []string{
		"stat", "-c", "%s|%Y|%a", p,
	}, nil)
	if err != nil {
		return executor.PathStat{}, err
	}
	if code != 0 {
		return executor.PathStat{}, fmt.Errorf("stat %q failed: exit %d", p, code)
	}
	return parseStat(p, string(out))
}

func parseStat(p, out string) (executor.PathStat, error) {
	fields := strings.Split(strings.TrimSpace(out), "|")
	if len(fields) != 3 {
		return executor.PathStat{}, fmt.Errorf("unexpected stat output %q", out)
	}
	size, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return executor.PathStat{}, fmt.Errorf("stat size %q: %w", fields[0], err)
	}
	mtime, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return executor.PathStat{}, fmt.Errorf("stat mtime %q: %w", fields[1], err)
	}
	mode, err := strconv.ParseUint(fields[2], 8, 32)
	if err != nil {
		return executor.PathStat{}, fmt.Errorf("stat mode %q: %w", fields[2], err)
	}
	return executor.PathStat{
		Name:  path.Base(p),
		Size:  size,
		Mode:  os.FileMode(mode),
		Mtime: time.Unix(mtime, 0),
	}, nil
}

// ListDir lists a directory via `find -print0`, then stats each entry (the same
// approach the Docker backend uses). Hidden entries are skipped; the result is
// capped at maxListEntries with Truncated set when more existed.
func (b *Backend) ListDir(ctx context.Context, id, dir string) (executor.DirListing, error) {
	out, _, code, err := b.execCapture(ctx, id, []string{
		"find", dir, "-maxdepth", "1", "-mindepth", "1", "!", "-name", ".*", "-print0",
	}, nil)
	if err != nil {
		return executor.DirListing{}, err
	}
	if code != 0 {
		return executor.DirListing{}, fmt.Errorf("list %q failed: exit %d", dir, code)
	}
	entries, truncated := parseFindEntries(out)
	listing := executor.DirListing{Truncated: truncated}
	for _, e := range entries {
		st, statErr := b.StatPath(ctx, id, e)
		if statErr != nil {
			listing.Failures = append(listing.Failures, executor.EntryError{Path: e, Err: statErr.Error()})
			continue
		}
		listing.Files = append(listing.Files, st)
	}
	return listing, nil
}

func parseFindEntries(output []byte) (entries []string, truncated bool) {
	for _, tok := range strings.Split(string(output), "\x00") {
		if tok == "" {
			continue
		}
		if len(entries) >= maxListEntries {
			return entries, true
		}
		entries = append(entries, tok)
	}
	return entries, false
}

// CopyIn writes a tar stream into dstDir via `tar -xf - -C dstDir` (exactly what
// `kubectl cp` does; k8s has no native copy API).
func (b *Backend) CopyIn(ctx context.Context, id, dstDir string, tar io.Reader) error {
	_, _, code, err := b.execCapture(ctx, id, []string{
		"tar", "-xf", "-", "-C", dstDir,
	}, tar)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("tar extract into %q failed: exit %d", dstDir, code)
	}
	return nil
}

// CopyOut streams a path out as a tar archive via `tar -cf - srcPath`. The
// returned reader is the live tar stream; PathStat is synthesized from a
// separate stat (moby returned it in a header, k8s does not).
func (b *Backend) CopyOut(ctx context.Context, id, srcPath string) (io.ReadCloser, executor.PathStat, error) {
	stat, err := b.StatPath(ctx, id, srcPath)
	if err != nil {
		return nil, executor.PathStat{}, err
	}
	pr, pw := io.Pipe()
	dir := path.Dir(srcPath)
	base := path.Base(srcPath)
	go func() {
		runErr := b.streamExec(ctx, id, executor.ExecSpec{
			Cmd: []string{"tar", "-cf", "-", "-C", dir, base},
		}, nil, pw, io.Discard)
		pw.CloseWithError(runErr)
	}()
	return pr, stat, nil
}
