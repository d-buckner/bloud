// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Daniel Buckner

package vm

import (
	"fmt"
)

// PreflightError contains details about a failed preflight check
type PreflightError struct {
	Check      string
	Message    string
	FixCommand string
	FixURL     string
}

func (e *PreflightError) Error() string {
	return e.Message
}

// PreflightResult holds the results of all preflight checks
type PreflightResult struct {
	Errors []PreflightError
}

func (r *PreflightResult) HasErrors() bool {
	return len(r.Errors) > 0
}

func (r *PreflightResult) AddError(check, message, fixCommand, fixURL string) {
	r.Errors = append(r.Errors, PreflightError{
		Check:      check,
		Message:    message,
		FixCommand: fixCommand,
		FixURL:     fixURL,
	})
}

// PrintPreflightErrors prints formatted preflight errors with fix instructions
func PrintPreflightErrors(result *PreflightResult) {
	fmt.Println()
	fmt.Println("\033[1;31m✗ Pre-flight checks failed\033[0m")
	fmt.Println()

	for i, err := range result.Errors {
		fmt.Printf("  \033[1;33m%d. %s\033[0m\n", i+1, err.Check)
		fmt.Printf("     %s\n", err.Message)
		if err.FixCommand != "" {
			fmt.Printf("     \033[36mFix:\033[0m %s\n", err.FixCommand)
		}
		if err.FixURL != "" {
			fmt.Printf("     \033[36mDocs:\033[0m %s\n", err.FixURL)
		}
		fmt.Println()
	}
}
