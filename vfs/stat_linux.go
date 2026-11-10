package vfs

import (
	"syscall"
	"time"

	"github.com/gulpjs/gulp/vinyl"
)

// statFromSys converts a stat result to a vinyl stat with the millisecond times of a Node.js Stats object.
func statFromSys(st *syscall.Stat_t) *vinyl.Stat {
	return &vinyl.Stat{
		Mode:  st.Mode,
		Size:  st.Size,
		Mtime: msTime(time.Unix(st.Mtim.Unix())),
		Atime: msTime(time.Unix(st.Atim.Unix())),
		Ctime: msTime(time.Unix(st.Ctim.Unix())),
	}
}
