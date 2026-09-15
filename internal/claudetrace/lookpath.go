package claudetrace

import "os/exec"

// execLookPath is a thin wrapper so slug.go can stay import-clean.
func execLookPath(bin string) (string, error) { return exec.LookPath(bin) }
