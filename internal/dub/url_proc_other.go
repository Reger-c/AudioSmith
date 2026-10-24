//go:build !unix

package dub

import "os/exec"

// ownGroup: elsewhere only the process is killed.
func ownGroup(cmd *exec.Cmd) {}

// KillOrphans: elsewhere nothing is looked for.
func KillOrphans(dir string) []int { return nil }
