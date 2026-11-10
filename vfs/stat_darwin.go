package vfs

import (
	"syscall"
	"time"

	"github.com/gulpjs/gulp/vinyl"
)

// statFromSys converts a stat result to a vinyl stat with the millisecond times of a Node.js Stats object.
func statFromSys(st *syscall.Stat_t) *vinyl.Stat {
	return &vinyl.Stat{
		Mode:  uint32(st.Mode),
		Size:  st.Size,
		Mtime: msTime(time.Unix(st.Mtimespec.Unix())),
		Atime: msTime(time.Unix(st.Atimespec.Unix())),
		Ctime: msTime(time.Unix(st.Ctimespec.Unix())),
	}
}
