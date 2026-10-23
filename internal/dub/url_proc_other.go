//go:build !unix

package dub

import "os/exec"

// ownGroup: elsewhere only the process is killed.
func ownGroup(cmd *exec.Cmd) {}
