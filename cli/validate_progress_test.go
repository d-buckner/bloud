// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"codeberg.org/d-buckner/bloud/cli/executor"
)

// The filters are the difference between a 10-minute integration phase that
// shows a blank console and one that shows what it is doing, so the tests
// pin the rendered lines rather than internals: a filter that silently stops
// matching the producers' real output is the failure mode, and it is silent.

func TestTestProgressRendersEachCase(t *testing.T) {
	in := strings.Join([]string{
		"=== RUN   TestSystemAppsConverged",
		"--- PASS: TestSystemAppsConverged (2.10s)",
		"=== RUN   TestJellyfinInstallViaAPI",
		"    jellyfin_test.go:33: waiting for the install operation",
		"--- FAIL: TestJellyfinInstallViaAPI (91.20s)",
		"=== RUN   TestRadicaleLDAPLogin",
		"--- SKIP: TestRadicaleLDAPLogin (0.00s)",
		"PASS",
		"",
	}, "\n")

	var buf bytes.Buffer
	p := newTestProgress(&buf, false)
	_, _ = p.Write([]byte(in))
	p.Finish()

	got := buf.String()
	for _, want := range []string{
		"▶ TestSystemAppsConverged",
		"✓ TestSystemAppsConverged",
		"✗ TestJellyfinInstallViaAPI",
		"⊘ TestRadicaleLDAPLogin",
		"1 passed, 1 failed, 1 skipped",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	// The runner's per-test chatter is not progress and must not reach the
	// console; it stays in the phase transcript instead.
	if strings.Contains(got, "waiting for the install operation") {
		t.Errorf("passing-test chatter leaked to the console:\n%s", got)
	}
	// The runner's own trailing PASS is not a test case.
	if strings.Contains(got, "3 passed") {
		t.Errorf("the runner's trailing PASS counted as a case:\n%s", got)
	}
}

func TestTestProgressKeepsDurations(t *testing.T) {
	var buf bytes.Buffer
	p := newTestProgress(&buf, false)
	_, _ = p.Write([]byte("--- PASS: TestMediaStackWiring (91.20s)\n"))
	p.Finish()

	if got := buf.String(); !strings.Contains(got, "91.20s") {
		t.Errorf("duration dropped from the case line:\n%s", got)
	}
}

func TestTestProgressNestsSubtests(t *testing.T) {
	var buf bytes.Buffer
	p := newTestProgress(&buf, false)
	_, _ = p.Write([]byte("=== RUN   TestCatalogUpdate_ImageBump/second_container\n"))
	_, _ = p.Write([]byte("    --- PASS: TestCatalogUpdate_ImageBump/second_container (0.45s)\n"))
	p.Finish()

	got := buf.String()
	if !strings.Contains(got, "  TestCatalogUpdate_ImageBump/second_container") {
		t.Errorf("subtest is not indented under its parent:\n%s", got)
	}
}

func TestTestProgressSaysWhenNothingRan(t *testing.T) {
	// A --test.run regex that matches nothing exits 0 and prints almost
	// nothing. Without this the CI leg looks like a suite that passed.
	var buf bytes.Buffer
	p := newTestProgress(&buf, false)
	_, _ = p.Write([]byte("testing: warning: no tests to run\nPASS\n"))
	p.Finish()

	if got := buf.String(); !strings.Contains(got, "no test cases ran") {
		t.Errorf("an empty run was not called out:\n%s", got)
	}
}

func TestProgressFiltersReassembleSplitLines(t *testing.T) {
	// Subprocess output arrives in arbitrary chunks; a JSON record or a test
	// result can straddle two writes.
	var buf bytes.Buffer
	p := newTestProgress(&buf, false)
	full := "--- PASS: TestSplitArrival (1.00s)\n"
	for i := 0; i < len(full); i += 7 {
		end := i + 7
		if end > len(full) {
			end = len(full)
		}
		_, _ = p.Write([]byte(full[i:end]))
	}
	p.Finish()

	if got := buf.String(); !strings.Contains(got, "✓ TestSplitArrival") {
		t.Errorf("a line split across writes was lost:\n%s", got)
	}
}

func TestProgressFiltersFlushTrailingPartialLine(t *testing.T) {
	var buf bytes.Buffer
	p := newTestProgress(&buf, false)
	_, _ = p.Write([]byte("--- PASS: TestNoTrailingNewline (0.50s)"))
	p.Finish()

	if got := buf.String(); !strings.Contains(got, "✓ TestNoTrailingNewline") {
		t.Errorf("a final line with no newline was dropped:\n%s", got)
	}
}

func TestAgentProgressRendersLifecyclePhases(t *testing.T) {
	in := strings.Join([]string{
		`{"time":"2026-10-04T19:41:53Z","level":"INFO","msg":"convergence step","step":"reconcile"}`,
		`{"time":"2026-10-04T19:41:54Z","level":"INFO","msg":"dispatching full lifecycle","app":"authentik-server","actual":"INITIALIZING","target":"RUNNING"}`,
		`{"time":"2026-10-04T19:41:55Z","level":"INFO","msg":"lifecycle phase: PreStart","app":"authentik-server"}`,
		`{"time":"2026-10-04T19:41:56Z","level":"INFO","msg":"lifecycle phase: PreStart complete","app":"authentik-server"}`,
		`{"time":"2026-10-04T19:41:57Z","level":"INFO","msg":"lifecycle phase: EnsureContainer","app":"authentik-server"}`,
		`{"time":"2026-10-04T19:41:58Z","level":"INFO","msg":"lifecycle phase: HealthCheck","app":"traefik"}`,
		`{"time":"2026-10-04T19:41:59Z","level":"INFO","msg":"marking node RUNNING after route generation","app":"authentik-server"}`,
		`{"time":"2026-10-04T19:42:00Z","level":"INFO","msg":"some agent chatter nobody asked for"}`,
		"",
	}, "\n")

	var buf bytes.Buffer
	p := newAgentProgress(&buf, false)
	_, _ = p.Write([]byte(in))
	p.Finish()

	got := buf.String()
	// Matched with flexible whitespace: the name field is padded so the phase
	// forms a column, and pinning the exact padding would make the test break
	// on a cosmetic change without telling us anything.
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`(?m)^ *· convergence\s+reconcile$`),
		regexp.MustCompile(`(?m)^ *· authentik-server\s+starting$`),
		regexp.MustCompile(`(?m)^ *· authentik-server\s+PreStart$`),
		regexp.MustCompile(`(?m)^ *· authentik-server\s+EnsureContainer$`),
		regexp.MustCompile(`(?m)^ *· traefik\s+HealthCheck$`),
		regexp.MustCompile(`(?m)^ *· authentik-server\s+RUNNING$`),
	} {
		if !want.MatchString(got) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "nobody asked for") {
		t.Errorf("an unmapped INFO record reached the console:\n%s", got)
	}
	// The `... complete` records restate the next start event; keeping both
	// would double every line of the phase list.
	if strings.Contains(got, "PreStart complete") {
		t.Errorf("the redundant completion record was rendered:\n%s", got)
	}
}

func TestAgentProgressAlwaysShowsWarnings(t *testing.T) {
	in := strings.Join([]string{
		`{"time":"2026-10-04T19:41:55Z","level":"WARN","msg":"route sync failed","error":"dynamic dir unreadable"}`,
		`{"time":"2026-10-04T19:41:56Z","level":"ERROR","msg":"failed to build app state","app":"jellyfin","error":"no such file"}`,
		"",
	}, "\n")

	var buf bytes.Buffer
	p := newAgentProgress(&buf, false)
	_, _ = p.Write([]byte(in))
	p.Finish()

	got := buf.String()
	if !strings.Contains(got, "WARN") || !strings.Contains(got, "route sync failed") {
		t.Errorf("warning dropped:\n%s", got)
	}
	if !strings.Contains(got, "dynamic dir unreadable") {
		t.Errorf("warning fields dropped -- a warning without its values is not actionable:\n%s", got)
	}
	if !strings.Contains(got, "ERROR") || !strings.Contains(got, "failed to build app state") {
		t.Errorf("error dropped:\n%s", got)
	}
}

func TestAgentProgressPassesNonJSONThrough(t *testing.T) {
	// A panic trace or a child process writing to the agent's stdout is the
	// one thing a filter must never swallow.
	var buf bytes.Buffer
	p := newAgentProgress(&buf, false)
	_, _ = p.Write([]byte("panic: runtime error: invalid memory address\n"))
	p.Finish()

	if got := buf.String(); !strings.Contains(got, "panic: runtime error") {
		t.Errorf("a non-JSON line was dropped:\n%s", got)
	}
}

func TestAgentProgressRendersPassComplete(t *testing.T) {
	var buf bytes.Buffer
	p := newAgentProgress(&buf, false)
	_, _ = p.Write([]byte(`{"level":"INFO","msg":"convergence pass complete","apps":6,"duration":"1m2s"}` + "\n"))
	p.Finish()

	if got := buf.String(); !strings.Contains(got, "pass complete (6 apps)") {
		t.Errorf("the pass-complete line did not render:\n%s", got)
	}
}

func TestPadFieldPadsWithoutTruncating(t *testing.T) {
	if got := padField("short", 10); got != "short     " {
		t.Errorf("no padding: %q", got)
	}
	if got := padField("exactly10!", 10); got != "exactly10!" {
		t.Errorf("exact width changed: %q", got)
	}
	// A long name is a test case's identity. Padding stops; the name never
	// loses characters, because "which case failed" is the one question the
	// console must always be able to answer.
	if got := padField("aaaaaaaaaaaaaaaaaaaa", 10); got != "aaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("a long name was truncated: %q", got)
	}
	if got := padField("héllo-wörld", 20); !strings.HasPrefix(got, "héllo-wörld") || len([]rune(got)) != 20 {
		t.Errorf("padding is not rune-aligned: %q", got)
	}
}

func TestProgressFiltersPaintOnlyWhenColored(t *testing.T) {
	var plain, painted bytes.Buffer
	for _, tc := range []struct {
		name  string
		out   *bytes.Buffer
		color bool
		want  bool
	}{{"plain", &plain, false, false}, {"color", &painted, true, true}} {
		p := newTestProgress(tc.out, tc.color)
		_, _ = p.Write([]byte("--- PASS: TestPaint (0.01s)\n"))
		p.Finish()
		has := strings.Contains(tc.out.String(), "\033[")
		if has != tc.want {
			t.Errorf("%s: ANSI escape present=%v, want %v\n%s", tc.name, has, tc.want, tc.out.String())
		}
	}
}

// testRunStep feeds a `go test -v` shaped stream through a phase so the
// wiring can be asserted end to end: live lines on the console, raw bytes in
// the tier log.
func testRunStep(out io.Writer) (int, error) {
	for _, l := range []string{
		"=== RUN   TestSomethingLong",
		"    x_test.go:9: chatter that belongs in the log, not the console",
		"--- PASS: TestSomethingLong (41.00s)",
	} {
		_, _ = io.WriteString(out, l+"\n")
	}
	return 0, nil
}

func TestPhaseLiveShowsProgressWhileThePhaseIsStillRunning(t *testing.T) {
	tr := newTestTranscript(t, validateFlags{})

	out := captureStdout(t, func() {
		if _, err := tr.phaseLive(manifestCommand{ID: "e2e-host-agent", Cwd: "/tmp", Run: "./bloud-integration.test"},
			newTestProgress(os.Stdout, false), testRunStep); err != nil {
			t.Errorf("unexpected phase error: %v", err)
		}
	})

	got := out
	if !strings.Contains(got, "▶ TestSomethingLong") {
		t.Errorf("the running test case never appeared on the console:\n%s", got)
	}
	if !strings.Contains(got, "✓ TestSomethingLong") {
		t.Errorf("the finished test case never appeared on the console:\n%s", got)
	}
	if strings.Contains(got, "chatter that belongs") {
		t.Errorf("raw runner chatter leaked past the filter:\n%s", got)
	}
	captureStdout(t, tr.finish)
	if log := readTierLog(t, tr); !strings.Contains(log, "chatter that belongs") {
		t.Errorf("the filter must not cost the log its evidence:\n%s", log)
	}
}

func TestPhaseLiveJSONModePrintsNothing(t *testing.T) {
	tr := newTestTranscript(t, validateFlags{json: true})

	out := captureStdout(t, func() {
		_, _ = tr.phaseLive(manifestCommand{ID: "e2e-host-agent", Cwd: "/tmp", Run: "./test"},
			newTestProgress(os.Stdout, false), testRunStep)
	})

	if out != "" {
		t.Errorf("JSON mode owns stdout; the filter must not write to it, got:\n%s", out)
	}
}

func TestPhaseLiveVerboseShowsRawAndSkipsTheFilter(t *testing.T) {
	// --verbose already streams everything, so a filtered line on top of the
	// raw line would print every test case twice.
	tr := newTestTranscript(t, validateFlags{verbose: true})

	out := captureStdout(t, func() {
		_, _ = tr.phaseLive(manifestCommand{ID: "e2e-host-agent", Cwd: "/tmp", Run: "./test"},
			newTestProgress(os.Stdout, false), testRunStep)
	})

	if strings.Count(out, "TestSomethingLong") != 2 {
		t.Errorf("--verbose should show the raw RUN and PASS lines and nothing filtered:\n%s", out)
	}
	// Exactly one tick: the phase's own verdict line. A second one would mean
	// the filter rendered on top of the raw stream and printed every case twice.
	if strings.Count(out, "✓") != 1 || strings.Contains(out, "▶") {
		t.Errorf("--verbose should show the raw stream and no filtered marks:\n%s", out)
	}
}

// waitFake stands in for the guest during the convergence wait: it streams a
// journal when asked to follow, and answers the health poll on command. It is
// what makes the follower/poll concurrency testable without a VM.
type waitFake struct {
	healthOK bool
	journal  []string
	mu       sync.Mutex
	ran      []string
}

// record notes a command the fake was asked to run. The follower and the poll
// run concurrently, so the record needs the lock the real executor does not
// need because it never shares one.
func (f *waitFake) record(cmd string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, cmd)
}

// commands returns a snapshot of what ran, taken under the lock.
func (f *waitFake) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ran...)
}

func (f *waitFake) Run(_ context.Context, spec executor.RunSpec) (executor.ExecResult, error) {
	f.record(spec.Command)
	if !f.healthOK {
		return executor.ExecResult{ExitCode: 1, Stdout: "deadline exceeded"}, errors.New("exit status 1")
	}
	return executor.ExecResult{ExitCode: 0}, nil
}

func (f *waitFake) RunStream(ctx context.Context, spec executor.RunSpec, stdout, _ io.Writer) error {
	f.record(spec.Command)
	if !strings.Contains(spec.Command, "journalctl") {
		return nil
	}
	for _, l := range f.journal {
		_, _ = io.WriteString(stdout, l+"\n")
	}
	// A real `journalctl -f` does not return until it is told to. The test
	// hangs if the wait phase forgets to cancel the follower.
	<-ctx.Done()
	return ctx.Err()
}

func (f *waitFake) CopyTo(context.Context, string, string) error   { return nil }
func (f *waitFake) CopyFrom(context.Context, string, string) error { return nil }

func TestWaitForAgentStreamsTheJournalWhilePollingHealth(t *testing.T) {
	f := &waitFake{healthOK: true, journal: []string{
		`{"level":"INFO","msg":"lifecycle phase: PreStart","app":"traefik"}`,
		`{"level":"INFO","msg":"marking node RUNNING after route generation","app":"traefik"}`,
	}}
	var live, transcript bytes.Buffer

	if err := integrationWaitForAgent(context.Background(), f, &transcript, &live); err != nil {
		t.Fatalf("wait should succeed: %v", err)
	}

	got := live.String()
	if !strings.Contains(got, "traefik") || !strings.Contains(got, "PreStart") {
		t.Errorf("the journal's lifecycle records never reached the live console:\n%s", got)
	}
	if !strings.Contains(got, "RUNNING") {
		t.Errorf("the node never showed as RUNNING:\n%s", got)
	}
	var followed bool
	for _, c := range f.commands() {
		if strings.Contains(c, "journalctl") && strings.Contains(c, integrationHostAgentUnit) {
			followed = true
		}
	}
	if !followed {
		t.Errorf("the follower should tail the validation unit, ran: %v", f.commands())
	}
}

func TestWaitForAgentKeepsTheTimeoutEvidenceInTheTranscript(t *testing.T) {
	// The failure path must not lose what the wait script dumped: that journal
	// tail is the whole reason the phase timed out.
	f := &waitFake{healthOK: false}
	var live, transcript bytes.Buffer

	err := integrationWaitForAgent(context.Background(), f, &transcript, &live)
	if err == nil {
		t.Fatal("a failed health poll must fail the phase")
	}
	if !strings.Contains(transcript.String(), "deadline exceeded") {
		t.Errorf("the timeout evidence did not reach the transcript:\n%s", transcript.String())
	}
}
