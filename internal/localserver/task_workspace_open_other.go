//go:build !unix

package localserver

import "os"

func openWorkspaceFile(path string) (*os.File, error) {
	return os.Open(path)
}

func openWorkspaceRootFile(root *os.Root, path string) (*os.File, error) {
	return root.Open(path)
}
