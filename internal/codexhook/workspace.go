package codexhook

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func relatedWorkspace(ctx context.Context, project, actual string) bool {
	if sameDirectory(project, actual) {
		return true
	}
	if !filepath.IsAbs(actual) || !filepath.IsAbs(project) {
		return false
	}
	// Only query Git metadata. Ignore repository overrides inherited from a shell.
	git := func(dir string, args ...string) string {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = []string{}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(kv), "GIT_") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		data, err := cmd.Output()
		if err != nil || len(data) > 2<<20 {
			return ""
		}
		return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	}
	top := git(project, "rev-parse", "--show-toplevel")
	other := git(actual, "rev-parse", "--show-toplevel")
	if top == "" || other == "" || !sameDirectory(git(project, "rev-parse", "--path-format=absolute", "--git-common-dir"), git(actual, "rev-parse", "--path-format=absolute", "--git-common-dir")) {
		return false
	}
	src, err := filepath.EvalSymlinks(project)
	if err != nil {
		return false
	}
	base, err := filepath.EvalSymlinks(top)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(base, src)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || !sameDirectory(filepath.Join(other, rel), actual) {
		return false
	}
	var sourceListed, targetListed bool
	for _, f := range strings.Split(git(project, "worktree", "list", "--porcelain", "-z"), "\x00") {
		if path, ok := strings.CutPrefix(f, "worktree "); ok {
			sourceListed = sourceListed || sameDirectory(path, top)
			targetListed = targetListed || sameDirectory(path, other)
		}
	}
	return sourceListed && targetListed
}
