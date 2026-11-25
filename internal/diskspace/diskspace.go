// Package diskspace reads the size and free space of the filesystem that
// holds a path, for the startup check of the tmpfs mounts and for model
// downloads.
package diskspace

// Info is a filesystem's size and the space available to this process.
type Info struct {
	TotalBytes int64
	FreeBytes  int64
}

// Check describes one directory and the per-user limit stored in it.
type Check struct {
	Name  string // setting that sets the limit, e.g. UPLOAD_MAX_TOTAL_BYTES
	Dir   string
	Limit int64
}

// Short reports whether the filesystem is smaller than the limit. One
// user filling the limit would then run out of space first.
func Short(info Info, limit int64) bool {
	return info.TotalBytes > 0 && info.TotalBytes < limit
}
