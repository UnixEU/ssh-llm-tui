//go:build linux || darwin || freebsd || netbsd || openbsd

package diskspace

import "syscall"

// Read returns the size and free space of the filesystem holding path.
func Read(path string) (Info, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Info{}, err
	}
	bs := int64(st.Bsize)
	return Info{TotalBytes: int64(st.Blocks) * bs, FreeBytes: int64(st.Bavail) * bs}, nil
}
