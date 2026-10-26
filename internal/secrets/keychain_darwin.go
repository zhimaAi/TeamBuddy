//go:build darwin

package secrets

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// keychainStore is a secret store backed by the macOS Keychain
type keychainStore struct {
	service string
}

// newStore factory: on macOS returns the Keychain implementation (the dir argument is ignored on this platform)
func newStore(dir string) (Store, error) {
	return &keychainStore{service: "goteams"}, nil
}

// Set stores a secret
func (s *keychainStore) Set(key, value string) error {
	key = withPrefix(key)
	cmd := exec.Command("security", "add-generic-password",
		"-s", s.service, "-a", key, "-w", value, "-U")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Keychain 写入失败: %w, output: %s", err, string(output))
	}
	return nil
}

// Get reads a secret
func (s *keychainStore) Get(key string) (string, error) {
	key = withPrefix(key)
	cmd := exec.Command("security", "find-generic-password",
		"-s", s.service, "-a", key, "-w")
	output, err := cmd.Output()
	if err != nil {
		// security 将错误写入 stderr，Output 的返回值只包含 stdout。
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.Contains(string(exitErr.Stderr), "could not be found") {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("Keychain 读取失败: %w", err)
	}
	// The security command output ends with a newline that must be stripped
	return strings.TrimRight(string(output), "\n\r"), nil
}

// Delete removes a secret
func (s *keychainStore) Delete(key string) error {
	key = withPrefix(key)
	cmd := exec.Command("security", "delete-generic-password",
		"-s", s.service, "-a", key)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "could not be found") {
			return nil // Secret does not exist, treated as delete success
		}
		return fmt.Errorf("Keychain 删除失败: %w, output: %s", err, string(output))
	}
	return nil
}

// Exists checks whether a secret exists
func (s *keychainStore) Exists(key string) (bool, error) {
	key = withPrefix(key)
	cmd := exec.Command("security", "find-generic-password",
		"-s", s.service, "-a", key)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "could not be found") {
			return false, nil
		}
		return false, fmt.Errorf("Keychain 查询失败: %w, output: %s", err, string(output))
	}
	return true, nil
}
