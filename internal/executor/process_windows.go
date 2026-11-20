//go:build windows

package executor

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const createNewProcessGroup = 0x00000200
const processExitVerificationTimeout = 2 * time.Second

// taskKillTimeout bounds the taskkill fallback. taskkill /T /F waits for every
// process in the tree to exit; a CLI blocked on an uninterruptible wait (e.g. an
// in-flight LLM network request) can otherwise hang the caller for minutes.
const taskKillTimeout = 3 * time.Second

// CreateProcessGroup sets process group attributes
func CreateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup,
	}
}

// TerminateProcessTree forcibly terminates the process tree.
//
// Primary path: TerminateProcess on every process in the tree. Unlike
// taskkill /T /F — which waits for each process to exit and can hang on a
// process stuck in an uninterruptible wait — TerminateProcess issues the
// termination immediately and returns without waiting.
//
// Fallback: taskkill /T /F (bounded by taskKillTimeout) for trees containing
// processes that reject direct termination (e.g. elevated children).
func TerminateProcessTree(pid int) error {
	if pid <= 0 {
		return nil
	}

	if err := terminateTreeImmediately(pid); err == nil {
		return nil
	}

	return terminateTreeWithTaskKill(pid)
}

// StopProbeProcessTree uses the same descendant-first Windows termination.
func StopProbeProcessTree(pid int) error { return TerminateProcessTree(pid) }

// terminateTreeImmediately force-kills every process in the tree via
// TerminateProcess without waiting for the targets to exit.
func terminateTreeImmediately(pid int) error {
	processIDs, err := collectProcessTreePIDs(pid)
	if err != nil {
		return err // 无法枚举整棵树时交给 taskkill，不伪称仅停止根进程即已完成。
	}

	var firstErr error
	// Children first (collectProcessTreePIDs is BFS from the root) so a dying
	// parent cannot spawn replacements before its children are gone.
	for i := len(processIDs) - 1; i >= 0; i-- {
		if err := terminatePID(processIDs[i]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return firstErr
	}
	survivors, err := waitForProcessesToExit(processIDs, processExitVerificationTimeout)
	if err != nil {
		return err
	}
	if len(survivors) > 0 {
		return syscall.EBUSY
	}
	return nil
}

// terminatePID forcibly terminates one process without waiting for exit.
// Already-exited processes are treated as success.
func terminatePID(processID int) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(processID))
	if err != nil {
		// Access denied 不能被当作进程已经退出，否则会残留仍在写文件的子进程。
		if err == windows.ERROR_INVALID_PARAMETER {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(handle)
	if err := windows.TerminateProcess(handle, 1); err != nil {
		return fmt.Errorf("终止进程 %d 失败: %w", processID, err)
	}
	return nil
}

// terminateTreeWithTaskKill is the fallback for trees containing processes that
// reject TerminateProcess (e.g. elevated children).
func terminateTreeWithTaskKill(pid int) error {
	// taskkill may return a non-zero exit code when some descendants have already exited or when another termination is not supported,
	// Even if the entire target process tree is actually cleaned. Record the process tree before calling, and use the actual survival after calling
	// PID shall prevail to avoid misreporting "part of the operation failed but finally exited" as a termination failure.
	targetPIDs, snapshotErr := collectProcessTreePIDs(pid)

	// taskkill /T will wait for and forcefully terminate the root process and all its descendants.
	// Bound the wait: when a descendant is stuck in an uninterruptible state, taskkill itself
	// blocks until that process exits, which can take minutes without a timeout.
	killCtx, killCancel := context.WithTimeout(context.Background(), taskKillTimeout)
	defer killCancel()
	killCmd := exec.CommandContext(killCtx, "taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", pid))
	output, err := killCmd.CombinedOutput()
	if snapshotErr == nil {
		survivors, verifyErr := waitForProcessesToExit(targetPIDs, processExitVerificationTimeout)
		if verifyErr == nil {
			if len(survivors) == 0 {
				return nil
			}
			detail := strings.TrimSpace(string(output))
			if detail == "" {
				detail = "taskkill 未提供错误详情"
			}
			return fmt.Errorf(
				"taskkill 未完全终止进程树 %d，仍存活 PID: %v: %s",
				pid, survivors, detail,
			)
		}
	}

	// Retain the original semantics of taskkill when a complete snapshot cannot be obtained; a successful return can still be directly regarded as a success.
	if err == nil {
		return nil
	}

	detail := strings.TrimSpace(string(output))
	if detail == "" {
		return fmt.Errorf("taskkill 终止进程树 %d 失败: %w", pid, err)
	}
	return fmt.Errorf("taskkill 终止进程树 %d 失败: %w: %s", pid, err, detail)
}

func collectProcessTreePIDs(rootPID int) ([]int, error) {
	parents, err := snapshotProcessParents()
	if err != nil {
		return nil, err
	}

	children := make(map[int][]int)
	for processID, parentID := range parents {
		children[parentID] = append(children[parentID], processID)
	}

	for parentID := range children {
		sort.Ints(children[parentID])
	}
	seen := map[int]bool{rootPID: true}
	processIDs := []int{rootPID}
	queue := []int{rootPID}
	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]
		for _, childPID := range children[parentID] {
			if seen[childPID] {
				continue
			}
			seen[childPID] = true
			processIDs = append(processIDs, childPID)
			queue = append(queue, childPID)
		}
	}

	// 返回 BFS 顺序而不是 PID 大小顺序，逆序终止才能保证先子后父。
	return processIDs, nil
}

func waitForProcessesToExit(processIDs []int, timeout time.Duration) ([]int, error) {
	targets := make(map[int]bool, len(processIDs))
	for _, processID := range processIDs {
		targets[processID] = true
	}

	deadline := time.Now().Add(timeout)
	for {
		parents, err := snapshotProcessParents()
		if err != nil {
			return nil, err
		}

		// There may still be descendants just forked during taskkill execution; as long as their parent PID belongs to the target tree,
		// Continue to include the verification to avoid missing newly generated orphan processes after the root process exits.
		for {
			added := false
			for processID, parentID := range parents {
				if targets[parentID] && !targets[processID] {
					targets[processID] = true
					added = true
				}
			}
			if !added {
				break
			}
		}

		survivors := make([]int, 0)
		for processID := range targets {
			if _, running := parents[processID]; running {
				survivors = append(survivors, processID)
			}
		}
		sort.Ints(survivors)
		if len(survivors) == 0 || !time.Now().Before(deadline) {
			return survivors, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func snapshotProcessParents() (map[int]int, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		if err == windows.ERROR_NO_MORE_FILES {
			return map[int]int{}, nil
		}
		return nil, err
	}

	parents := make(map[int]int)
	for {
		parents[int(entry.ProcessID)] = int(entry.ParentProcessID)
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if err == windows.ERROR_NO_MORE_FILES {
				return parents, nil
			}
			return nil, err
		}
	}
}
