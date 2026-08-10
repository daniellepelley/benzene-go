//go:build unix

package healthcheck

import "syscall"

// diskUsage reports free (available-to-unprivileged) and total bytes for the filesystem containing
// path, via a single statfs(2) syscall - zero-dependency, standard library only. Bavail is the blocks
// available to an ordinary process (below Bfree, which counts the root-reserved blocks); Bsize is the
// fundamental block size, whose Go type differs across unix variants (int64 on Linux, uint32 on
// Darwin), so it is widened through uint64 which covers both.
func diskUsage(path string) (free, total uint64, err error) {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return 0, 0, err
	}
	bsize := uint64(fs.Bsize)
	return uint64(fs.Bavail) * bsize, uint64(fs.Blocks) * bsize, nil
}
