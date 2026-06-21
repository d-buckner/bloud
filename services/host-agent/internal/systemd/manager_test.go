package systemd

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordedCommand struct {
	name string
	args []string
}

type fakeRunner struct {
	commands []recordedCommand
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.commands = append(f.commands, recordedCommand{name: name, args: args})
	return nil, nil
}

func TestUserManagerConvergesAndRestartsChangedUnit(t *testing.T) {
	runner := &fakeRunner{}
	manager := newManager(true, runner)

	require.NoError(t, manager.Reload(context.Background()))
	require.NoError(t, manager.EnsureRunning(context.Background(), "apps-jellyfin.service", true))
	require.NoError(t, manager.Stop(context.Background(), "apps-jellyfin.service"))

	assert.Equal(t, []recordedCommand{
		{name: "systemctl", args: []string{"--user", "daemon-reload"}},
		{name: "systemctl", args: []string{"--user", "restart", "apps-jellyfin.service"}},
		{name: "systemctl", args: []string{"--user", "stop", "apps-jellyfin.service"}},
	}, runner.commands)
}

func TestSystemManagerStartsUnchangedUnit(t *testing.T) {
	runner := &fakeRunner{}
	manager := newManager(false, runner)

	require.NoError(t, manager.EnsureRunning(context.Background(), "apps-jellyfin.service", false))

	assert.Equal(t, []recordedCommand{
		{name: "systemctl", args: []string{"start", "apps-jellyfin.service"}},
	}, runner.commands)
}
