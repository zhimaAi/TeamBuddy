// Package codebuddy provides CodeBuddy Code CLI execution adaptation.
package codebuddy

import (
	"goteams-client/internal/executor"
	"goteams-client/internal/executor/claude"
)

const displayName = "CodeBuddy Code"

// NewAdapter creates the CodeBuddy CLI adapter.
//
// CodeBuddy's stream-json, session recovery and permission parameters use the same event contract as Claude Code.
// Therefore reuse the proven streaming event parsing and process tree termination implementation.
func NewAdapter() executor.Adapter {
	return claude.NewStreamJSONAdapter(displayName, true)
}
