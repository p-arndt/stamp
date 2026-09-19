// Package hook runs the user-defined commands from the `hooks:` block of
// .stamp.yml.
//
// A hook is a command line, not a program name, so it goes through the
// platform's shell: `sh -c` on unix, and on Windows PowerShell 7 (`pwsh`) when
// it is installed, `cmd /C` otherwise. The output is streamed as it comes, so a
// slow `cargo update` shows its progress instead of looking hung.
package hook

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Env is what a hook is told about the release, as environment variables.
type Env struct {
	Version   string // STAMP_VERSION: the version just written
	Previous  string // STAMP_PREVIOUS_VERSION: the version before it
	Component string // STAMP_COMPONENT: empty in a single-version repository
}

// Shell returns the program and the leading arguments a command line is
// appended to on this platform.
func Shell() (string, []string) {
	if runtime.GOOS != "windows" {
		return "sh", []string{"-c"}
	}
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path, []string{"-NoProfile", "-Command"}
	}
	return "cmd", []string{"/C"}
}

// Run executes command in dir through the platform shell and waits for it.
// A non-zero exit is an error naming the command.
func Run(dir, command string, env Env) error {
	shell, args := Shell()
	cmd := exec.Command(shell, append(args, command)...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		"STAMP_VERSION="+env.Version,
		"STAMP_PREVIOUS_VERSION="+env.Previous,
		"STAMP_COMPONENT="+env.Component,
	)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook `%s` failed: %w", command, err)
	}
	return nil
}
