package gitshim

import (
	"io/fs"
	"os"
)

// defaultStat is the real os.Stat, wrapped so statOS is swappable in
// tests that want to fake "is this dir a git repo".
func defaultStat(path string) (fs.FileInfo, error) { return os.Stat(path) }
