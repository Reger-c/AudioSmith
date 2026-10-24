//go:build unix

package dub

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

// KillOrphans kills what yt-dlp runs an earlier AudioSmith left behind
// (#101 L4): it ran in a process group of its own, so a crash or a
// forced quit of the app did not end it, and it would go on writing the
// source a new fetch writes too. Only processes started from dir (the
// app's tools folder) are touched, and only those that are not this
// process's children; each is killed with its group (yt-dlp's launcher,
// Python, ffmpeg). It returns the pids killed.
func KillOrphans(dir string) []int { return killOrphans(dir, os.Getpid()) }

func killOrphans(dir string, parent int) []int {
	if dir == "" {
		return nil
	}
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,pgid=,args=").Output()
	if err != nil {
		return nil
	}
	prefix := filepath.Clean(dir) + string(filepath.Separator)
	self := syscall.Getpgrp()
	var killed []int
	for _, line := range strings.Split(string(out), "\n") {
		var nums [3]int
		rest, ok := line, true
		for i := range nums {
			var field string
			field, rest, _ = strings.Cut(strings.TrimLeft(rest, " "), " ")
			n, err := strconv.Atoi(field)
			if err != nil {
				ok = false
				break
			}
			nums[i] = n
		}
		pid, ppid, pgid := nums[0], nums[1], nums[2]
		if !ok || pid == os.Getpid() || ppid == parent {
			continue
		}
		args := strings.TrimLeft(rest, " ")
		if !strings.HasPrefix(args, prefix) {
			continue
		}
		if pgid > 1 && pgid != self {
			syscall.Kill(-pgid, syscall.SIGKILL)
		} else {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		killed = append(killed, pid)
	}
	return killed
}
