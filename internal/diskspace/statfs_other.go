//go:build !(linux || darwin || freebsd || netbsd || openbsd)

package diskspace

import "errors"

// Read is not available on this platform.
func Read(string) (Info, error) {
	return Info{}, errors.New("disk space is not available on this platform")
}
