// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/graph"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/engine/orchestrator"
	"codeberg.org/d-buckner/bloud/services/host-agent/internal/testdb"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

func newMinimalOrchestrator(t *testing.T) *orchestrator.Orchestrator {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registry := configurator.NewRegistry(logger, configurator.Deps{})
	return orchestrator.NewOrchestrator(
		graph.New(graph.NewMapRepository()),
		registry,
		nil, // no catalog: minimal wiring
		t.TempDir(),
		logger,
		orchestrator.OrchestratorConfig{},
	)
}

// CheckSystemHealth must not report healthy when the orchestrator's intent
// loop has exited: every Submit answers 202 while nothing reconciles, and
// the old db.Ping()-only check was blind to it.
func TestCheckSystemHealth_SeesDeadOrchestratorLoop(t *testing.T) {
	db := testdb.SetupTestDB(t)
	orch := newMinimalOrchestrator(t)
	srv := &Server{db: db, orch: orch}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan struct{})
	go func() {
		orch.Start(ctx)
		close(returned)
	}()
	<-orch.Ready()

	require.NoError(t, srv.CheckSystemHealth(), "a running loop is healthy")

	cancel()
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("orchestrator loop did not exit after cancel")
	}

	err := srv.CheckSystemHealth()
	require.Error(t, err, "a dead loop must fail the health check")
	assert.Contains(t, err.Error(), "orchestrator")
}
