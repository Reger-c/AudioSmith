//go:build unix

package dub

import (
	"os/exec"
	"syscall"
)

// ownGroup starts cmd in a process group of its own, and makes its
// cancellation kill the whole group: yt-dlp's macOS build is a launcher
// that runs Python in a child, which runs ffmpeg; killing the launcher
// alone left them running (and the pipes open).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil {
			err = cmd.Process.Kill()
		}
		return err
	}
}
