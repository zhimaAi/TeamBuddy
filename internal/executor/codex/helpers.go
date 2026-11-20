package codex

import (
	"os"
	"os/exec"

	"goteams-client/internal/executor"
)

func environ() []string {
	return os.Environ()
}

func createProcessGroupForCmd(cmd *exec.Cmd) {
	executor.CreateProcessGroup(cmd)
}
