package installer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"codeberg.org/d-buckner/bloud-v3/services/installer/internal/nixinstall"
	"codeberg.org/d-buckner/bloud-v3/services/installer/internal/partition"
)

type Phase string

const (
	PhaseIdle         Phase = "idle"
	PhaseValidating   Phase = "validating"
	PhasePartitioning Phase = "partitioning"
	PhaseFormatting   Phase = "formatting"
	PhaseInstalling   Phase = "installing"
	PhaseConfiguring  Phase = "configuring"
	PhaseComplete     Phase = "complete"
	PhaseFailed       Phase = "failed"
)

type LogEvent struct {
	Phase   Phase  `json:"phase"`
	Message string `json:"message"`
}

type InstallRequest struct {
	Disk       string `json:"disk"`
	Encryption bool   `json:"encryption"`
	FlakePath  string `json:"flakePath"`
}

type Installer struct {
	mu          sync.Mutex
	phase       Phase
	subscribers []chan LogEvent
}

func New() *Installer {
	return &Installer{
		phase: PhaseIdle,
	}
}

func (inst *Installer) Phase() Phase {
	inst.mu.Lock()
	defer inst.mu.Unlock()
	return inst.phase
}

func (inst *Installer) Emit(phase Phase, message string) {
	inst.mu.Lock()
	inst.phase = phase
	subs := make([]chan LogEvent, len(inst.subscribers))
	copy(subs, inst.subscribers)
	inst.mu.Unlock()

	event := LogEvent{Phase: phase, Message: message}
	for _, ch := range subs {
		select {
		case ch <- event:
		default:
		}
	}
}

func (inst *Installer) Subscribe() (<-chan LogEvent, func()) {
	ch := make(chan LogEvent, 64)

	inst.mu.Lock()
	inst.subscribers = append(inst.subscribers, ch)
	inst.mu.Unlock()

	unsub := func() {
		inst.mu.Lock()
		defer inst.mu.Unlock()
		for i, s := range inst.subscribers {
			if s == ch {
				inst.subscribers = append(inst.subscribers[:i], inst.subscribers[i+1:]...)
				break
			}
		}
		close(ch)
	}

	return ch, unsub
}

// StartMock runs a fake installation that emits realistic events with delays.
// Used when INSTALLER_MOCK=1 for local development on non-Linux machines.
func (inst *Installer) StartMock(req InstallRequest) error {
	inst.mu.Lock()
	if inst.phase != PhaseIdle && inst.phase != PhaseFailed && inst.phase != PhaseComplete {
		inst.mu.Unlock()
		return fmt.Errorf("installation already in progress (phase: %s)", inst.phase)
	}
	inst.phase = PhaseValidating
	inst.subscribers = nil // reset subscribers so stale channels don't accumulate
	inst.mu.Unlock()

	go func() {
		steps := []struct {
			phase   Phase
			message string
			delay   time.Duration
		}{
			{PhaseValidating, "Validating parameters", 800 * time.Millisecond},
			{PhasePartitioning, "Clearing existing signatures from " + req.Disk, time.Second},
			{PhasePartitioning, "Creating GPT partition table", time.Second},
			{PhasePartitioning, "Creating EFI partition (1MiB–513MiB)", time.Second},
			{PhasePartitioning, "Creating root partition (513MiB–100%)", time.Second},
			{PhaseFormatting, "Formatting EFI partition as FAT32", time.Second},
			{PhaseFormatting, "Formatting root partition as ext4", time.Second},
			{PhaseFormatting, "Mounting partitions", 500 * time.Millisecond},
			{PhaseInstalling, "Starting nixos-install", time.Second},
			{PhaseInstalling, "unpacking nixos-system-bloud-25.05pre...", 2 * time.Second},
			{PhaseInstalling, "copying path '/nix/store/...-linux-6.6.66' to '/mnt'...", 2 * time.Second},
			{PhaseInstalling, "copying path '/nix/store/...-glibc-2.39' to '/mnt'...", 2 * time.Second},
			{PhaseInstalling, "copying path '/nix/store/...-bloud-host-agent-0.1.0' to '/mnt'...", 2 * time.Second},
			{PhaseInstalling, "activating the configuration...", time.Second},
			{PhaseConfiguring, "Applying post-install configuration", time.Second},
			{PhaseConfiguring, fmt.Sprintf("Encryption: %v", req.Encryption), 300 * time.Millisecond},
		{PhaseConfiguring, "User account setup deferred to post-reboot wizard", 500 * time.Millisecond},
			{PhaseComplete, "Installation complete — ready to reboot", 0},
		}

		for _, step := range steps {
			time.Sleep(step.delay)
			inst.Emit(step.phase, step.message)
		}
	}()

	return nil
}

func (inst *Installer) Start(ctx context.Context, req InstallRequest) error {
	inst.mu.Lock()
	if inst.phase != PhaseIdle && inst.phase != PhaseFailed {
		inst.mu.Unlock()
		return fmt.Errorf("installation already in progress (phase: %s)", inst.phase)
	}
	inst.phase = PhaseValidating
	inst.mu.Unlock()

	go inst.run(ctx, req)
	return nil
}

func (inst *Installer) run(ctx context.Context, req InstallRequest) {
	inst.Emit(PhaseValidating, "Validating installation parameters")

	if req.Disk == "" {
		inst.Emit(PhaseFailed, "no disk specified")
		return
	}

	emit := func(message string) {
		inst.Emit(inst.Phase(), message)
	}

	inst.Emit(PhasePartitioning, "Partitioning disk "+req.Disk)
	emitPartition := func(msg string) {
		inst.Emit(PhasePartitioning, msg)
	}

	if err := partition.Prepare(ctx, req.Disk, emitPartition); err != nil {
		inst.Emit(PhaseFailed, "partitioning failed: "+err.Error())
		return
	}

	inst.Emit(PhaseFormatting, "Formatting complete")

	inst.Emit(PhaseInstalling, "Starting NixOS installation")
	emitInstall := func(msg string) {
		inst.Emit(PhaseInstalling, msg)
	}

	flakePath := req.FlakePath
	if flakePath == "" {
		flakePath = "/etc/bloud"
	}

	if err := nixinstall.Install(ctx, flakePath, emitInstall); err != nil {
		inst.Emit(PhaseFailed, "nixos-install failed: "+err.Error())
		return
	}

	inst.Emit(PhaseConfiguring, "Applying post-install configuration")
	emit("User account setup deferred to post-reboot wizard")

	inst.Emit(PhaseComplete, "Installation complete — ready to reboot")
}
