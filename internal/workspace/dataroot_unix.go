//go:build !windows

package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// realHomeDir 返回用户主目录。非 Windows 平台 Home 位置稳定，直接读取。
func realHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return home
	}
	return filepath.Clean(home)
}

// deviceKeyOf 返回文件信息的设备号标识（unix 跨盘判断）。
func deviceKeyOf(info os.FileInfo) any {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Dev
	}
	return nil
}

// existingAncestorDev 返回路径最近已存在祖先的设备号（unix）。
func existingAncestorDev(p string) any {
	p = filepath.Clean(p)
	for {
		info, err := os.Stat(p)
		if err == nil {
			return deviceKeyOf(info)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}

// sameVol 判断两个路径是否在同一卷（决定搬迁方式是 rename 还是复制+删除）。
func sameVol(a, b string) bool {
	devA := existingAncestorDev(a)
	devB := existingAncestorDev(b)
	if devA == nil || devB == nil {
		return devA == nil && devB == nil
	}
	return devA == devB
}

// isElectronProcessName 命中 Electron 桌面宿主的进程名（开发模式）。
func isElectronProcessName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.Contains(name, "electron")
}

// processName 返回指定 PID 的命令名。
// 不能依赖 /proc（macOS 没有），ps -o comm= 在两个 unix 桌面平台行为一致。
func processName(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return ""
	}
	return filepath.Base(strings.TrimSpace(string(out)))
}

// parentPIDOf 返回指定 PID 的父进程 ID。
func parentPIDOf(pid int) int {
	if pid <= 0 {
		return 0
	}
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=").Output()
	if err != nil {
		return 0
	}
	ppid, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return ppid
}


// canonicalizeWindowsTarget 是 Windows 专用逻辑，unix 下永不触发。
func canonicalizeWindowsTarget(target string) (string, bool) { return target, false }

// volumeSerialNumber 是 Windows 专用逻辑，unix 下永不触发。
func volumeSerialNumber(string) (uint32, error) { return 0, nil }
