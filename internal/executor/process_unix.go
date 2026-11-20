//go:build !windows

package executor

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// snapshotProcessParents 读取当前进程表，返回 pid 到父 pid 的映射。
// 读不到时由调用方按「子进程还在」处理，避免误杀正在编译的工具。
func snapshotProcessParents() (map[int]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,stat=").Output()
	if err != nil {
		return nil, err
	}
	parents := make(map[int]int)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || strings.HasPrefix(fields[2], "Z") {
			continue
		}
		pid, errPID := strconv.Atoi(fields[0])
		ppid, errPPID := strconv.Atoi(fields[1])
		if errPID != nil || errPPID != nil {
			continue
		}
		parents[pid] = ppid
	}
	return parents, nil
}

// CreateProcessGroup sets process group attributes
func CreateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// terminateGracePeriod 是 SIGTERM 之后等待进程自己退出的时间。
// 进程一旦退出就立刻返回，不会无条件睡满这段时间。
const terminateGracePeriod = 5 * time.Second

// terminateKillWait 是 SIGKILL 之后确认进程组清空的时间。
const terminateKillWait = 5 * time.Second

// terminatePollInterval The interval for polling whether the process group has exited.
const terminatePollInterval = 50 * time.Millisecond

// TerminateProcessTree terminates the process tree.
//
// First SIGTERM gives the process a chance to clean up by itself, and then polls for detection; once the process group exits, it will return immediately.
func TerminateProcessTree(pgid int) error {
	return terminateProcessGroup(pgid, terminateGracePeriod, terminateKillWait)
}

// StopProbeProcessTree ends a read-only probe after its RPC pipes have closed.
// The caller reaps the root with Wait; polling an unreaped PID for seconds on
// the interactive launch path adds latency without protecting user work.
func StopProbeProcessTree(pgid int) error {
	return terminateProcessGroup(pgid, 100*time.Millisecond, 0)
}

func terminateProcessGroup(pgid int, termGrace, killWait time.Duration) error {
	if pgid <= 0 {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		// 继续尝试强杀。ESRCH 表示进程组已经不存在。
	}
	if waitGroupGone(pgid, termGrace) {
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return fmt.Errorf("SIGKILL 失败: %w", err)
	}
	waitGroupGone(pgid, killWait)
	return nil
}

func waitGroupGone(pgid int, grace time.Duration) bool {
	if grace <= 0 {
		return syscall.Kill(-pgid, 0) == syscall.ESRCH
	}
	deadline := time.Now().Add(grace)
	for {
		if err := syscall.Kill(-pgid, 0); err == syscall.ESRCH {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(terminatePollInterval)
	}
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
