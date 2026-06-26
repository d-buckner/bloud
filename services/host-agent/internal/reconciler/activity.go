package reconciler

import (
	"sync"
	"sync/atomic"
	"time"
)

const maxActivityEntries = 50

// ActivityEntry represents a single reconciler event for observability.
type ActivityEntry struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"`  // "intent_enqueued", "drain_complete", "converge_start", "converge_step", "converge_complete"
	Detail string    `json:"detail"` // e.g. "InstallApp:jellyfin", "step:ensure-apps", "7 apps, 312ms"
}

// ActivityLog is a thread-safe ring buffer that stores the last 50 reconciler events.
// A watermark tracks the start of the active window so Recent() only returns
// events from the current cycle. Checkpoint() advances the watermark to the
// head, effectively hiding all prior events from API consumers.
type ActivityLog struct {
	mu        sync.RWMutex
	entries   []ActivityEntry
	pos       int // next write position (head)
	watermark int // start of active window (tail)
}

// NewActivityLog creates an empty activity log.
func NewActivityLog() *ActivityLog {
	return &ActivityLog{
		entries: make([]ActivityEntry, maxActivityEntries),
	}
}

// Record adds an event to the activity log.
func (a *ActivityLog) Record(event, detail string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.entries[a.pos] = ActivityEntry{
		Time:   time.Now(),
		Event:  event,
		Detail: detail,
	}
	a.pos = (a.pos + 1) % maxActivityEntries

	// If head catches up to watermark, advance watermark so we never
	// return stale entries that have been overwritten.
	if a.pos == a.watermark {
		a.watermark = (a.watermark + 1) % maxActivityEntries
	}
}

// Checkpoint advances the watermark to the current head position, hiding all
// prior events from subsequent Recent() calls.
func (a *ActivityLog) Checkpoint() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.watermark = a.pos
}

// Recent returns activity entries newest-first, only from the active window
// (watermark to head).
func (a *ActivityLog) Recent() []ActivityEntry {
	a.mu.RLock()
	defer a.mu.RUnlock()

	count := (a.pos - a.watermark + maxActivityEntries) % maxActivityEntries

	result := make([]ActivityEntry, count)
	for i := range count {
		idx := (a.pos - 1 - i + maxActivityEntries) % maxActivityEntries
		result[i] = a.entries[idx]
	}
	return result
}

// AppPhase represents the current lifecycle phase of a single app during convergence.
type AppPhase struct {
	AppName string `json:"appName"`
	Phase   string `json:"phase"`  // "pre-start", "ensure-container", "health-check", "post-start", "sso"
	Status  string `json:"status"` // "active", "done", "error", "warning"
}

// Status represents the reconciler's current state for the developer API.
type Status struct {
	QueueDepth     int             `json:"queueDepth"`
	IsConverging   bool            `json:"isConverging"`
	RecentActivity []ActivityEntry `json:"recentActivity"`
	AppPhases      []AppPhase      `json:"appPhases,omitempty"`
}

// ReconcilerStatus holds the converging flag and activity log needed to
// produce a Status snapshot. Embedded in the Reconciler struct.
type reconcilerStatus struct {
	activity   *ActivityLog
	converging atomic.Bool
	appPhases  sync.Map // map["appName:phase"]*AppPhase
}

// setAppPhase updates the current phase and status for an app.
func (s *reconcilerStatus) setAppPhase(appName, phase, status string) {
	s.appPhases.Store(appName+":"+phase, &AppPhase{
		AppName: appName,
		Phase:   phase,
		Status:  status,
	})
}

// clearAppPhases removes all app phase entries (called at start of convergence).
func (s *reconcilerStatus) clearAppPhases() {
	s.appPhases.Range(func(key, _ any) bool {
		s.appPhases.Delete(key)
		return true
	})
}

// getAppPhases returns a snapshot of all current app phases.
func (s *reconcilerStatus) getAppPhases() []AppPhase {
	var phases []AppPhase
	s.appPhases.Range(func(_, value any) bool {
		if ap, ok := value.(*AppPhase); ok {
			phases = append(phases, *ap)
		}
		return true
	})
	return phases
}
