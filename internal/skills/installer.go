package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	TaskSkillName = "teambuddy"
	// S-BE005, DA-003: ImBotCLISkillName 是 teambuddy-im-bot-cli 内置 Skill 的名称
	ImBotCLISkillName                = "teambuddy-im-bot-cli"
	CapabilityReasonNotManaged       = "skill_not_managed"
	CapabilityReasonInstallationFail = "skill_install_failed"
	managedMarker                    = ".goteams-managed.json"
)

var (
	ErrTargetNotManaged   = errors.New("同名 Skill 已存在且不由 TeamBuddy 管理")
	ErrManagedUserChanged = errors.New("TeamBuddy 托管 Skill 已被用户修改")
	errManagedCurrent     = errors.New("TeamBuddy 托管 Skill 已是最新版本")
)

type managedMetadata struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// InstallBuiltin synchronizes only the built-in Skill owned by TeamBuddy. It
// never traverses or changes sibling directories below the skill root.
// A skill root that is a symlink is resolved once to a real directory inside
// the user home; later writes use that path, so replacing the original link
// cannot redirect the install. The teambuddy directory itself must still be
// a real directory.
func InstallBuiltin(sourceFS fs.FS, targetRoot string) error {
	root, err := writableSkillRoot(targetRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("创建 Skill 根目录失败: %w", err)
	}
	return installOne(sourceFS, root, TaskSkillName)
}

// skillInstallHome is the home directory used to bound a resolved skill root.
var skillInstallHome = os.UserHomeDir

// writableSkillRoot returns the directory that receives the managed skill.
// Missing roots are created later as real directories. An existing symlink is
// accepted only when its final target is a real directory inside the user home.
func writableSkillRoot(targetRoot string) (string, error) {
	info, err := os.Lstat(targetRoot)
	if os.IsNotExist(err) {
		return targetRoot, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		if !info.IsDir() {
			return "", fmt.Errorf("Skill 根目录不是安全目录")
		}
		return targetRoot, nil
	}
	resolved, err := filepath.EvalSymlinks(targetRoot)
	if err != nil {
		return "", fmt.Errorf("Skill 根目录不是安全目录")
	}
	resolvedInfo, err := os.Lstat(resolved)
	if err != nil || resolvedInfo.Mode()&os.ModeSymlink != 0 || !resolvedInfo.IsDir() {
		return "", fmt.Errorf("Skill 根目录不是安全目录")
	}
	if !pathWithinHome(resolved) {
		return "", fmt.Errorf("Skill 根目录不是安全目录")
	}
	return resolved, nil
}

func pathWithinHome(path string) bool {
	home, err := skillInstallHome()
	if err != nil {
		return false
	}
	home = canonicalizePath(home)
	path = canonicalizePath(path)
	rel, err := filepath.Rel(home, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func canonicalizePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Clean(path)
}

func installOne(sourceFS fs.FS, targetRoot, name string) error {
	sourceDigest, err := sourceDirectoryDigest(sourceFS, name)
	if err != nil {
		return err
	}
	target := filepath.Join(targetRoot, name)
	if err := validateInstallTarget(targetRoot, target, name, sourceDigest); err != nil {
		if errors.Is(err, errManagedCurrent) {
			return nil
		}
		return err
	}

	stage, err := os.MkdirTemp(targetRoot, "."+name+"-stage-")
	if err != nil {
		return fmt.Errorf("创建 Skill 临时目录失败: %w", err)
	}
	defer os.RemoveAll(stage)
	if err := copySourceDirectory(sourceFS, name, stage); err != nil {
		return err
	}
	metadata, _ := json.Marshal(managedMetadata{Name: name, Digest: sourceDigest})
	if err := os.WriteFile(filepath.Join(stage, managedMarker), metadata, 0o600); err != nil {
		return fmt.Errorf("写入 Skill 托管标记失败: %w", err)
	}

	backup, err := os.MkdirTemp(targetRoot, "."+name+"-backup-")
	if err != nil {
		return fmt.Errorf("创建 Skill 备份路径失败: %w", err)
	}
	if err := os.Remove(backup); err != nil {
		return fmt.Errorf("准备 Skill 备份路径失败: %w", err)
	}
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("备份旧 Skill 失败: %w", err)
		}
	}
	if err := os.Rename(stage, target); err != nil {
		if _, backupErr := os.Lstat(backup); backupErr == nil {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("安装内置 Skill 失败: %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("清理 Skill 备份失败: %w", err)
	}
	return nil
}

func validateInstallTarget(targetRoot, target, name, sourceDigest string) error {
	rootInfo, err := os.Lstat(targetRoot)
	if err != nil {
		return err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return fmt.Errorf("Skill 根目录不是安全目录")
	}
	info, err := os.Lstat(target)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrTargetNotManaged, name)
	}
	targetDigest, err := diskDirectoryDigest(target)
	if err != nil {
		return err
	}
	markerRaw, markerErr := os.ReadFile(filepath.Join(target, managedMarker))
	if os.IsNotExist(markerErr) {
		return fmt.Errorf("%w: %s", ErrTargetNotManaged, name)
	}
	if markerErr != nil {
		return fmt.Errorf("%w: %s 的托管标记不可读", ErrTargetNotManaged, name)
	}
	var marker managedMetadata
	if json.Unmarshal(markerRaw, &marker) != nil || marker.Name != name || !isValidDigest(marker.Digest) {
		return fmt.Errorf("%w: %s", ErrTargetNotManaged, name)
	}
	if targetDigest != marker.Digest {
		return fmt.Errorf("%w: %s", ErrManagedUserChanged, name)
	}
	if targetDigest == sourceDigest {
		return errManagedCurrent
	}
	return nil
}

func isValidDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func copySourceDirectory(sourceFS fs.FS, name, stage string) error {
	return fs.WalkDir(sourceFS, name, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("内置 Skill 不允许包含符号链接: %s", sourcePath)
		}
		relative, err := filepath.Rel(name, sourcePath)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(stage, relative)
		if entry.IsDir() {
			return os.MkdirAll(targetPath, 0o700)
		}
		data, err := fs.ReadFile(sourceFS, sourcePath)
		if err != nil {
			return err
		}
		return os.WriteFile(targetPath, data, 0o600)
	})
}

func sourceDirectoryDigest(sourceFS fs.FS, name string) (string, error) {
	entries := make(map[string][]byte)
	err := fs.WalkDir(sourceFS, name, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("内置 Skill 不允许包含符号链接: %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(name, path)
		if err != nil {
			return err
		}
		entries[filepath.ToSlash(relative)], err = fs.ReadFile(sourceFS, path)
		return err
	})
	if err != nil {
		return "", fmt.Errorf("读取内置 Skill 失败: %w", err)
	}
	return digestEntries(entries), nil
}

func diskDirectoryDigest(root string) (string, error) {
	entries := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: Skill 目录不允许包含符号链接: %s", ErrTargetNotManaged, path)
		}
		if entry.IsDir() || entry.Name() == managedMarker {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries[filepath.ToSlash(relative)], err = os.ReadFile(path)
		return err
	})
	if err != nil {
		return "", err
	}
	return digestEntries(entries), nil
}

func digestEntries(entries map[string][]byte) string {
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, path := range paths {
		hash.Write([]byte(strings.ReplaceAll(path, "\\", "/")))
		hash.Write([]byte{0})
		hash.Write(entries[path])
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// S-BE005, DA-003: InstallImBotCLISkill 安装 teambuddy-im-bot-cli 内置 Skill
// 复用 InstallBuiltin 的机制，将 teambuddy-im-bot-cli 目录复制到用户 skill 目录
func InstallImBotCLISkill(sourceFS fs.FS, targetRoot string) error {
	root, err := writableSkillRoot(targetRoot)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("创建 Skill 根目录失败: %w", err)
	}
	return installOne(sourceFS, root, ImBotCLISkillName)
}
