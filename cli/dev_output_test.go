// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// newBufConsole builds a console whose console-stream and log-stream are both
// capturable buffers. color is off because a bytes.Buffer is not a terminal,
// which is the same rule that keeps piped output plain.
func newBufConsole(t *testing.T) (*devConsole, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	log := &bytes.Buffer{}
	c := &devConsole{out: out, log: log, started: time.Now()}
	return c, out, log
}

// discardConsole is a console that goes nowhere, for tests that exercise the
// reload loop's control flow rather than its output.
func discardConsole() *devConsole {
	return &devConsole{out: io.Discard, log: io.Discard}
}

func TestHeaderJoinsPartsAndDropsBlanks(t *testing.T) {
	c, out, _ := newBufConsole(t)
	c.Header("native host", "", "http://localhost:8080")

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("header should be one line, got %q", out.String())
	}
	got := lines[0]
	if !strings.Contains(got, "Bloud dev") || !strings.Contains(got, "native host") || !strings.Contains(got, "http://localhost:8080") {
		t.Errorf("header = %q, want the title, backend, and URL", got)
	}
	if strings.Contains(got, "· ·") {
		t.Errorf("an empty part left an empty separator: %q", got)
	}
}

func TestStepPadsToAFixedColumn(t *testing.T) {
	c, out, _ := newBufConsole(t)
	c.Step("provision runtime", "native host", 200*time.Millisecond)
	c.Step("build host-agent", "linux/amd64", 1400*time.Millisecond)
	c.Step("host-agent", "API http://localhost:3000", 0)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 step lines, got %d: %q", len(lines), out.String())
	}
	durations := []string{"200ms", "1.4s"}
	for i, want := range durations {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want duration %q", i, lines[i], want)
		}
	}
	// The duration column has to line up, or the block reads as a mess. The
	// values are right-aligned, so it is the right edge that is shared.
	end0 := strings.Index(lines[0], "200ms") + len("200ms")
	end1 := strings.Index(lines[1], "1.4s") + len("1.4s")
	if end0 != end1 {
		t.Errorf("durations are not right-aligned to the same column: %d vs %d (%q / %q)", end0, end1, lines[0], lines[1])
	}
	// A step with no measured duration simply omits the column.
	if strings.Contains(lines[2], "  0s") {
		t.Errorf("a zero duration should be omitted, got %q", lines[2])
	}
	if !strings.Contains(lines[2], "API http://localhost:3000") {
		t.Errorf("step detail missing from %q", lines[2])
	}
}

func TestStepFailedCarriesTheError(t *testing.T) {
	c, out, _ := newBufConsole(t)
	c.StepFailed("build host-agent", "linux/amd64", time.Second, fmt.Errorf("exit status 1"))

	got := out.String()
	if !strings.Contains(got, "build host-agent") || !strings.Contains(got, "exit status 1") {
		t.Errorf("failed step = %q, want the step name and the error", got)
	}
	if !strings.Contains(got, devMarkFail) {
		t.Errorf("failed step should carry the failure mark, got %q", got)
	}
}

func TestConsoleIsPlainTextWithoutATerminal(t *testing.T) {
	c, out, _ := newBufConsole(t)
	c.Step("ready", "http://localhost:8080", time.Second)
	if strings.Contains(out.String(), "\033[") {
		t.Errorf("a non-terminal console must not emit ANSI codes: %q", out.String())
	}
}

func TestReloadOKIsOneLine(t *testing.T) {
	c, out, _ := newBufConsole(t)
	c.ReloadOK("services/host-agent/internal/api/server.go", 1900*time.Millisecond)

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("a successful reload should be exactly one line, got %d: %q", len(lines), out.String())
	}
	for _, want := range []string{"services/host-agent/internal/api/server.go", "rebuilt 1.9s", "reloaded", "containers untouched"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("reload line %q missing %q", lines[0], want)
		}
	}
}

func TestReloadFailedShowsTheTail(t *testing.T) {
	c, out, _ := newBufConsole(t)
	c.ReloadFailed("apps/jellyfin/configurator.go", "configurator.go:8:2: undefined: nope\n\tsecond line")

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("a failed reload needs the detail lines too, got %q", out.String())
	}
	if !strings.Contains(lines[0], "build failed") || !strings.Contains(lines[0], "left up") {
		t.Errorf("the first line should say the running host-agent survived: %q", lines[0])
	}
	if !strings.Contains(lines[1], "undefined: nope") {
		t.Errorf("the compile error should be printed, got %q", lines[1])
	}
}

func TestSurfaceHostAgentLogKeepsWarningsOnly(t *testing.T) {
	info, _ := json.Marshal(map[string]any{"time": "2026-01-01T10:00:00Z", "level": "info", "msg": "intent enqueued", "app": "traefik"})
	warn, _ := json.Marshal(map[string]any{"time": "2026-01-01T10:00:01Z", "level": "warn", "msg": "gateway not available", "component": "authentik"})
	errRec, _ := json.Marshal(map[string]any{"level": "error", "msg": "boom", "error": "connection refused"})

	if _, _, ok := surfaceHostAgentLog(string(info)); ok {
		t.Error("an INFO record must not reach the quiet console")
	}
	level, msg, ok := surfaceHostAgentLog(string(warn))
	if !ok || level != "WARN" {
		t.Fatalf("warn record should surface as WARN, got %q %v", level, ok)
	}
	if !strings.Contains(msg, "gateway not available") || !strings.Contains(msg, "component=authentik") {
		t.Errorf("surfaced warn = %q, want the message and its attributes", msg)
	}
	if level, msg, ok := surfaceHostAgentLog(string(errRec)); !ok || level != "ERROR" || !strings.Contains(msg, "connection refused") {
		t.Errorf("error record surfaced as %q %q ok=%v", level, msg, ok)
	}
}

func TestSurfaceHostAgentLogIgnoresNonJSON(t *testing.T) {
	for _, line := range []string{"", "   ", "plain text", "{not json", "{\"level\":}"} {
		if _, _, ok := surfaceHostAgentLog(line); ok {
			t.Errorf("non-JSON line %q should not surface", line)
		}
	}
}

func TestSurfaceDashboardLogKeepsFailures(t *testing.T) {
	if _, _, ok := surfaceDashboardLog("  VITE v6.3.5  ready in 512 ms"); ok {
		t.Error("the vite banner is noise and must not surface")
	}
	if _, _, ok := surfaceDashboardLog("[vite] hmr update /src/routes/+page.svelte"); ok {
		t.Error("routine HMR chatter must not surface")
	}
	for _, line := range []string{
		"Pre-transform error: Unexpected token",
		"Error: Cannot find module './x'",
		"Error: listen EADDRINUSE 127.0.0.1:5173",
	} {
		level, msg, ok := surfaceDashboardLog(line)
		if !ok || level != "ERROR" {
			t.Errorf("dashboard failure %q should surface, got %q ok=%v", line, level, ok)
		}
		if !strings.HasPrefix(msg, "dashboard: ") {
			t.Errorf("dashboard lines should be attributed, got %q", msg)
		}
	}
}

func TestStreamTeeMirrorsEverythingToTheLog(t *testing.T) {
	c, out, log := newBufConsole(t)
	w := c.QuietStream()
	_, _ = io.WriteString(w, "go: downloading foo\nsome noise\n")

	if got := log.String(); !strings.Contains(got, "go: downloading foo") || !strings.Contains(got, "some noise") {
		t.Errorf("the log must receive every byte, got %q", got)
	}
	if out.Len() != 0 {
		t.Errorf("a quiet stream must not reach the console, got %q", out.String())
	}
}

func TestStreamTeeSurfacesOnlyPickedLines(t *testing.T) {
	c, out, _ := newBufConsole(t)
	w := c.HostAgentStream()
	info, _ := json.Marshal(map[string]any{"level": "info", "msg": "all good"})
	warn, _ := json.Marshal(map[string]any{"level": "warn", "msg": "not all good"})
	_, _ = w.Write([]byte(string(info) + "\n" + string(warn) + "\n"))

	got := out.String()
	if strings.Contains(got, "all good\nnot all good") || strings.Count(got, "good") != 1 {
		t.Errorf("only the warning should surface: %q", got)
	}
	if !strings.Contains(got, "WARN") || !strings.Contains(got, "not all good") {
		t.Errorf("surfaced output = %q", got)
	}
}

func TestStreamTeeVerboseWritesRawToTheConsole(t *testing.T) {
	c, out, log := newBufConsole(t)
	c.verbose = true
	w := c.HostAgentStream()
	_, _ = io.WriteString(w, "{\"level\":\"info\",\"msg\":\"raw through\"}\n")

	if got := out.String(); !strings.Contains(got, "\"level\":\"info\"") {
		t.Errorf("verbose mode should pass raw bytes through, got %q", got)
	}
	if !strings.Contains(log.String(), "raw through") {
		t.Error("verbose mode must still mirror to the log")
	}
}

func TestStreamTeeHoldsPartialLinesUntilNewline(t *testing.T) {
	c, out, log := newBufConsole(t)
	w := c.DashboardStream()
	_, _ = io.WriteString(w, "Error: someth")

	if out.Len() != 0 {
		t.Errorf("a partial line must not be surfaced before it ends, got %q", out.String())
	}
	if !strings.Contains(log.String(), "Error: someth") {
		t.Error("the log gets partial lines too; it is a byte mirror")
	}
	_, _ = io.WriteString(w, "ing broken\n")
	if !strings.Contains(out.String(), "Error: something broken") {
		t.Errorf("the completed line should surface, got %q", out.String())
	}
}

func TestStreamTeeTailIsBoundedAndOrdered(t *testing.T) {
	c, _, _ := newBufConsole(t)
	w := c.QuietStream()
	for i := 0; i < 40; i++ {
		_, _ = fmt.Fprintf(w, "line %d\n", i)
	}
	tail := w.Tail(5)
	if len(tail) != 5 {
		t.Fatalf("Tail(5) returned %d lines: %v", len(tail), tail)
	}
	want := []string{"line 35", "line 36", "line 37", "line 38", "line 39"}
	for i, s := range want {
		if tail[i] != s {
			t.Errorf("tail[%d] = %q, want %q", i, tail[i], s)
		}
	}
	if got := w.Tail(0); got != nil {
		t.Errorf("Tail(0) = %v, want nil", got)
	}
}

func TestSuppressAgentLogHidesWarningsButNotTheLog(t *testing.T) {
	c, out, log := newBufConsole(t)
	warn, _ := json.Marshal(map[string]any{"level": "warn", "msg": "sql: database is closed"})

	c.SuppressAgentLog(true)
	_, _ = io.WriteString(c.HostAgentStream(), string(warn)+"\n")
	if out.Len() != 0 {
		t.Errorf("a muted agent stream must not reach the console, got %q", out.String())
	}
	if !strings.Contains(log.String(), "database is closed") {
		t.Error("the log keeps the record regardless of the mute")
	}

	c.SuppressAgentLog(false)
	_, _ = io.WriteString(c.HostAgentStream(), string(warn)+"\n")
	if !strings.Contains(out.String(), "database is closed") {
		t.Errorf("after unmuting, warnings surface again, got %q", out.String())
	}
}

func TestSuppressAgentLogDoesNotAffectOtherStreams(t *testing.T) {
	c, out, _ := newBufConsole(t)
	c.SuppressAgentLog(true)
	_, _ = io.WriteString(c.DashboardStream(), "Error: something broken\n")
	if !strings.Contains(out.String(), "something broken") {
		t.Errorf("only the agent stream is muted; dashboard errors still show, got %q", out.String())
	}
}

func TestPrintTailSkipsBlankLines(t *testing.T) {
	c, out, _ := newBufConsole(t)
	c.PrintTail([]string{"first", "", "   ", "second"})

	got := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(got) != 2 {
		t.Fatalf("blank tail lines should be dropped, got %q", out.String())
	}
	if !strings.Contains(got[0], "first") || !strings.Contains(got[1], "second") {
		t.Errorf("tail = %q", got)
	}
}

func TestHintLogPathOnlyPrintsWhenThereIsAFile(t *testing.T) {
	c, out, _ := newBufConsole(t)
	c.HintLogPath()
	if out.Len() != 0 {
		t.Errorf("no log path should print nothing, got %q", out.String())
	}
	c.logPath = "/tmp/dev.log"
	c.HintLogPath()
	if !strings.Contains(out.String(), "/tmp/dev.log") {
		t.Errorf("hint = %q, want the log path", out.String())
	}
}

func TestDevDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, ""},
		{-time.Second, ""},
		{5 * time.Millisecond, "5ms"},
		{950 * time.Millisecond, "950ms"},
		{2 * time.Second, "2.0s"},
		{90 * time.Second, "90.0s"},
	}
	for _, tc := range cases {
		if got := devDuration(tc.in); got != tc.want {
			t.Errorf("devDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestClipMarksTruncation(t *testing.T) {
	if got := clip("short", 10); got != "short" {
		t.Errorf("clip under the limit = %q", got)
	}
	got := clip(strings.Repeat("x", 20), 10)
	if len(got) != 10 || !strings.HasSuffix(got, "...") {
		t.Errorf("clip = %q, want 10 chars ending in ...", got)
	}
}

func TestDevLogPathIsRepoLocal(t *testing.T) {
	got := devLogPath("/home/dev/bloud")
	if got != "/home/dev/bloud/.bloud/logs/dev.log" {
		t.Errorf("devLogPath = %q", got)
	}
}

func TestRotateDevLogLeavesASingleBackup(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dev.log"
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), devLogRotateBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	rotateDevLog(path)
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("the old log should have been moved aside: %v", err)
	}
}

func TestRotateDevLogLeavesASmallLogAlone(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/dev.log"
	if err := os.WriteFile(path, []byte("tiny"), 0o644); err != nil {
		t.Fatal(err)
	}
	rotateDevLog(path)
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Errorf("a log under the rotation size should not be moved, stat err = %v", err)
	}
}

func TestNewDevConsoleWritesToTheLogPath(t *testing.T) {
	path := t.TempDir() + "/nested/logs/dev.log"
	var out bytes.Buffer
	c, err := newDevConsole(&out, path, false)
	if err != nil {
		t.Fatalf("newDevConsole: %v", err)
	}
	_, _ = io.WriteString(c.QuietStream(), "mirrored\n")
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(raw), "mirrored") {
		t.Errorf("log contents = %q, want the mirrored line", raw)
	}
}
