package orchestrator

import (
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/store"
)

// newTestOrchestratorWithWorker creates an orchestrator with the debounce worker running.
// debounce controls how long the worker waits after the last signal before firing.
func newTestOrchestratorWithWorker(debounce time.Duration) *testOrchestrator {
	to := newTestOrchestratorWithMocks()
	to.orch.outpostDebounce = debounce
	to.orch.outpostSignal = make(chan struct{}, 1)
	to.orch.outpostDone = make(chan struct{})
	go to.orch.outpostAssociationWorker()
	return to
}

func TestTriggerOutpostAssociation_SingleTrigger_CallsOnce(t *testing.T) {
	to := newTestOrchestratorWithWorker(10 * time.Millisecond)
	defer to.orch.Stop()

	to.appStore.On("GetAll").Return([]*store.InstalledApp{
		fixtureInstalledApp("adguard-home", "running"),
	}, nil)
	to.cache.On("Get", "adguard-home").Return(fixtureAdguardHome(), nil)
	to.authentikClient.On("AddProviderToEmbeddedOutpost", "AdGuard Home Proxy Provider").Return(nil)

	to.orch.triggerOutpostAssociation()

	time.Sleep(50 * time.Millisecond)

	to.authentikClient.AssertNumberOfCalls(t, "AddProviderToEmbeddedOutpost", 1)
}

func TestTriggerOutpostAssociation_RapidTriggers_CoalescedIntoOneCall(t *testing.T) {
	to := newTestOrchestratorWithWorker(20 * time.Millisecond)
	defer to.orch.Stop()

	to.appStore.On("GetAll").Return([]*store.InstalledApp{
		fixtureInstalledApp("adguard-home", "running"),
	}, nil)
	to.cache.On("Get", "adguard-home").Return(fixtureAdguardHome(), nil)
	to.authentikClient.On("AddProviderToEmbeddedOutpost", mock.Anything).Return(nil)

	// Fire 5 rapid triggers
	for range 5 {
		to.orch.triggerOutpostAssociation()
	}

	time.Sleep(100 * time.Millisecond)

	to.authentikClient.AssertNumberOfCalls(t, "AddProviderToEmbeddedOutpost", 1)
}

func TestStop_TriggerAfterStopDoesNotPanicOrFire(t *testing.T) {
	to := newTestOrchestratorWithWorker(10 * time.Millisecond)
	to.authentikClient.On("AddProviderToEmbeddedOutpost", mock.Anything).Return(nil).Maybe()

	to.orch.Stop()

	// Trigger after stop — must not panic, must not result in an API call
	to.orch.triggerOutpostAssociation()
	time.Sleep(50 * time.Millisecond)

	to.authentikClient.AssertNotCalled(t, "AddProviderToEmbeddedOutpost")
}

func TestTriggerOutpostAssociation_TriggerAfterDebounce_FiresAgain(t *testing.T) {
	to := newTestOrchestratorWithWorker(10 * time.Millisecond)
	defer to.orch.Stop()

	to.appStore.On("GetAll").Return([]*store.InstalledApp{
		fixtureInstalledApp("adguard-home", "running"),
	}, nil)
	to.cache.On("Get", "adguard-home").Return(fixtureAdguardHome(), nil)
	to.authentikClient.On("AddProviderToEmbeddedOutpost", mock.Anything).Return(nil)

	// First trigger — fires after debounce
	to.orch.triggerOutpostAssociation()
	time.Sleep(50 * time.Millisecond)

	// Second trigger after the window — must fire again, not be dropped
	to.orch.triggerOutpostAssociation()
	time.Sleep(50 * time.Millisecond)

	to.authentikClient.AssertNumberOfCalls(t, "AddProviderToEmbeddedOutpost", 2)
}
