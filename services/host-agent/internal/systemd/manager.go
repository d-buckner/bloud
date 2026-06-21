package systemd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Manager exposes only the systemd operations needed by runtime adapters.
type Manager interface {
	Reload(ctx context.Context) error
	EnsureRunning(ctx context.Context, unit string, restart bool) error
	Stop(ctx context.Context, unit string) error
}

type commandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// SystemManager controls either the system or user systemd manager.
type SystemManager struct {
	user   bool
	runner commandRunner
}

func NewManager(user bool) *SystemManager {
	return &SystemManager{user: user, runner: execRunner{}}
}

func newManager(user bool, runner commandRunner) *SystemManager {
	return &SystemManager{user: user, runner: runner}
}

func (m *SystemManager) Reload(ctx context.Context) error {
	return m.run(ctx, "daemon-reload")
}

func (m *SystemManager) EnsureRunning(ctx context.Context, unit string, restart bool) error {
	if restart {
		return m.run(ctx, "restart", unit)
	}
	return m.run(ctx, "start", unit)
}

func (m *SystemManager) Stop(ctx context.Context, unit string) error {
	return m.run(ctx, "stop", unit)
}

func (m *SystemManager) run(ctx context.Context, args ...string) error {
	if m.user {
		args = append([]string{"--user"}, args...)
	}
	output, err := m.runner.Run(ctx, "systemctl", args...)
	if err != nil {
		return fmt.Errorf("systemctl %s failed: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}
