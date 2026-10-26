//go:build unix

package localserver

import (
	"os"
	"syscall"
)

func openWorkspaceFile(path string) (*os.File, error) {
	// 防止类型检查后文件被替换为 FIFO，或最终节点被替换为符号链接。
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}

func openWorkspaceRootFile(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}
