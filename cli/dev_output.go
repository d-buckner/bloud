// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The dev console.
//
// `./bloud dev` streams four unrelated producers into one terminal: the CLI's
// own orchestration, the Go build, the dashboard's vite dev server, and the
// host-agent's structured log (300+ call sites, one JSON object per line).
// Piped straight through, that is unreadable, and the lines a developer
// actually needs, "the reload failed and here is why", are the ones that get
// buried.
//
// The rule this renderer applies is that the console carries decisions and the
// log file carries evidence. Every raw byte from every subprocess is mirrored
// to a dev log whose path is printed once at bring-up; the console shows the
// bring-up steps, one line per reload, and only the log lines that are a
// warning or worse. `--verbose` opts back in to the raw stream.
const (
	// devStepColumn is where the padded "name + detail" field of a step line
	// ends, so the durations below it line up into a column.
	devStepColumn = 44
	// devDurationWidth right-aligns the duration inside this many columns.
	devDurationWidth = 8

	// devTailLines is how many recent raw lines a stream keeps so a failed
	// step can print what actually killed it. A Go compile error is a handful
	// of lines; more than this and the tail stops being a summary.
	devTailLines    = 24
	devTailLineChar = 200

	// devLineBufferLimit bounds the partial-line buffer. A producer that emits
	// no newline at all must not grow the buffer without end; the overflow is
	// flushed as a single long line instead.
	devLineBufferLimit = 1 << 16

	// devLogRotateBytes is the size past which the previous dev log is moved
	// aside to a single `.1` file before a run appends to a fresh one.
	devLogRotateBytes = 5 << 20

	// devQuietProgressFirst / devQuietProgressRest pace the "still starting"
	// note. The first pass on a cold stack takes minutes, and repeating the
	// same sentence every 15s is noise: say it once when it stops being
	// instant, then often enough that a long wait is still visibly alive.
	devQuietProgressFirst = 15 * time.Second
	devQuietProgressRest  = 60 * time.Second
)

// devConsole renders the dev loop's output and owns the dev log.
//
// It is safe for concurrent use: the reload loop, the readiness watcher, and
// the subprocess tee writers all write through it, and a torn interleaving of
// two partial lines is exactly the mess this exists to prevent.
type devConsole struct {
	mu      sync.Mutex
	out     io.Writer
	log     io.Writer
	logFile *os.File
	logPath string
	verbose bool
	color   bool
	started time.Time
	// agentMuted silences the host-agent stream on the console while the CLI
	// is deliberately stopping that process. See SuppressAgentLog.
	agentMuted bool
}

// newDevConsole builds the renderer for one dev run. An empty logPath keeps
// the raw mirror on io.Discard, which is what the tests use; every real run
// passes a path so the evidence survives the console.
func newDevConsole(out io.Writer, logPath string, verbose bool) (*devConsole, error) {
	c := &devConsole{
		out:     out,
		log:     io.Discard,
		logPath: logPath,
		verbose: verbose,
		color:   devColorEnabled(out),
		started: time.Now(),
	}
	if logPath == "" {
		return c, nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("create the dev log dir: %w", err)
	}
	rotateDevLog(logPath)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open the dev log: %w", err)
	}
	c.log = f
	c.logFile = f
	return c, nil
}

// devLogPath is where a run's full raw output lands. It is a CLI-side path on
// purpose: on the VM backends the runtime directories live in the guest, but
// the developer is sitting at the machine that ran `./bloud dev`.
func devLogPath(root string) string {
	return filepath.Join(root, ".bloud", "logs", "dev.log")
}

// rotateDevLog moves a previous log aside so one run cannot grow the file
// forever. Best-effort: a log that cannot be rotated is still a log.
func rotateDevLog(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() < devLogRotateBytes {
		return
	}
	_ = os.Rename(path, path+".1")
}

// devColorEnabled reports whether w is a terminal worth coloring. Piped output
// and CI logs get plain text, which is what makes the log file and a
// redirected console readable.
func devColorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// paint wraps s in an ANSI color when the console has one.
func (c *devConsole) paint(s, color string) string {
	if color == "" || !c.color {
		return s
	}
	return color + s + colorReset
}

// writeLine emits one finished line to the console under the lock.
func (c *devConsole) writeLine(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = io.WriteString(c.out, line+"\n")
}

// Header prints the run's identity: what is being run, on what, and where it
// will be reachable.
func (c *devConsole) Header(parts ...string) {
	filled := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			filled = append(filled, p)
		}
	}
	c.writeLine(c.paint("Bloud dev", colorCyan) + " · " + strings.Join(filled, " · "))
	c.writeLine("")
}

// Blank prints an empty line to separate blocks of output.
func (c *devConsole) Blank() {
	c.writeLine("")
}

// HintLogPath tells the developer where the full raw output is, when there is
// a file behind it. Printed once at bring-up: it is the answer to "where do I
// look when this is not enough", and repeating it would be the noise this
// renderer exists to remove.
func (c *devConsole) HintLogPath() {
	if c.logPath != "" {
		c.Hint("full output: " + c.logPath)
	}
}

// PrintTail prints captured raw lines under a step, dim and indented. It is
// how a failed build becomes actionable without opening the log file.
func (c *devConsole) PrintTail(lines []string) {
	for _, l := range detailLines(joinTail(lines), devTailLines) {
		c.writeLine("      " + c.paint(l, colorDim))
	}
}

// Note prints an indented plain line: context that belongs on the console but
// is not a step.
func (c *devConsole) Note(msg string) {
	c.writeLine("  " + msg)
}

// Hint prints an indented dim line: information a developer reads once, like
// where the log file is.
func (c *devConsole) Hint(msg string) {
	c.writeLine("  " + c.paint(msg, colorDim))
}

// Step prints a completed bring-up step with how long it took.
func (c *devConsole) Step(name, detail string, dur time.Duration) {
	c.stepLine(devMarkOK, colorGreen, name, detail, dur)
}

// StepFailed prints a step that did not complete, with the error inline so the
// failure is legible without reading the log file.
func (c *devConsole) StepFailed(name, detail string, dur time.Duration, err error) {
	if err != nil {
		if detail != "" {
			detail += ": " + err.Error()
		} else {
			detail = err.Error()
		}
	}
	c.stepLine(devMarkFail, colorRed, name, detail, dur)
}

const (
	devMarkOK   = "✓"
	devMarkFail = "✗"
	devMarkRun  = "⟳"
)

// stepLine lays out `  <mark> <name>  <detail>` padded to devStepColumn with
// the duration right-aligned after it. The mark is colored before the padding
// is computed so its escape codes cannot shift the column.
func (c *devConsole) stepLine(mark, color, name, detail string, dur time.Duration) {
	left := name
	if detail != "" {
		left += "  " + detail
	}
	line := "  " + c.paint(mark, color) + " " + padRight(left, devStepColumn)
	if ds := devDuration(dur); ds != "" {
		line += "  " + c.paint(padLeft(ds, devDurationWidth), colorDim)
	}
	c.writeLine(strings.TrimRight(line, " "))
}

// devDuration formats a duration the way the console shows it: sub-second
// values in milliseconds, everything else in seconds with one decimal.
func devDuration(dur time.Duration) string {
	if dur <= 0 {
		return ""
	}
	if dur < time.Second {
		rounded := dur.Round(time.Millisecond)
		if rounded <= 0 {
			return ""
		}
		return rounded.String()
	}
	return fmt.Sprintf("%.1fs", dur.Seconds())
}

// ReloadOK reports a completed reload on one line: what triggered it, how
// long the build took, and the fact that no app container was disturbed.
func (c *devConsole) ReloadOK(trigger string, buildDur time.Duration) {
	msg := fmt.Sprintf("%s → rebuilt %s · reloaded · containers untouched",
		trigger, fallbackStr(devDuration(buildDur), "instantly"))
	c.writeLine("  " + c.paint(devMarkRun+" "+c.timestamp(), colorDim) + "  " +
		c.paint(msg, colorGreen))
}

// ReloadFailed reports a reload that did not happen, plus the tail of the
// build output that explains it. The running host-agent is deliberately left
// up, and the line says so, because "is my dashboard down?" is the first
// question a build failure raises.
func (c *devConsole) ReloadFailed(trigger string, detail string) {
	c.writeLine("  " + c.paint(devMarkRun+" "+c.timestamp(), colorDim) + "  " +
		c.paint(trigger+" → build failed · running host-agent left up", colorRed))
	for _, l := range detailLines(detail, devTailLines) {
		c.writeLine("      " + c.paint(l, colorDim))
	}
}

// Emit prints one surfaced log line with its level. This is the quiet console's
// view of the host-agent: warnings and errors only, timestamped, no JSON.
func (c *devConsole) Emit(level, msg string) {
	color := ""
	switch level {
	case "ERROR", "FATAL":
		color = colorRed
	case "WARN", "WARNING":
		color = colorYellow
	}
	tag := level
	if tag == "" {
		tag = "LOG"
	}
	c.writeLine("  " + c.paint(c.timestamp(), colorDim) + "  " + c.paint(tag, color) + "  " + msg)
}

// timestamp is the console's clock for surfaced log and reload lines.
func (c *devConsole) timestamp() string {
	return time.Now().Format("15:04:05")
}

// Stream returns a writer for one subprocess. Everything written to it is
// mirrored to the dev log; surface decides what the quiet console shows, and
// a verbose console shows the raw bytes instead.
func (c *devConsole) Stream(surface surfaceFunc) *streamTee {
	return &streamTee{console: c, surface: surface}
}

// HostAgentStream carries the host-agent's structured log: only WARN and above
// reach a quiet console.
//
// The mute check lives here rather than inside surfaceHostAgentLog so that
// filter stays a pure function of one line, and "we caused this shutdown" stays
// a property of the console rather than of the log record.
func (c *devConsole) HostAgentStream() *streamTee {
	return c.Stream(func(line string) (string, string, bool) {
		if c.agentLogMuted() {
			return "", "", false
		}
		return surfaceHostAgentLog(line)
	})
}

// SuppressAgentLog mutes the host-agent stream on the console while the CLI is
// deliberately stopping the host-agent.
//
// Tearing the process down out from under its own orchestrator produces a
// burst of warnings on every single reload, because the recorder is still
// writing when the database closes. Those warnings are real, and they are in
// the log; showing them once per reload is how a console teaches you to stop
// reading it. Warnings from a host-agent that is actually running, including
// one that fails to come back up, are untouched.
func (c *devConsole) SuppressAgentLog(on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.agentMuted = on
}

func (c *devConsole) agentLogMuted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.agentMuted
}

// DashboardStream carries vite. Its banner and HMR chatter are noise; a Svelte
// compile error is not.
func (c *devConsole) DashboardStream() *streamTee {
	return c.Stream(surfaceDashboardLog)
}

// QuietStream keeps a stream off a quiet console entirely. Build output is
// useless until it fails, and a failing caller prints the tail instead.
func (c *devConsole) QuietStream() *streamTee {
	return c.Stream(noSurface)
}

// surfaceFunc decides whether one raw line earns a place on the quiet console,
// returning the level tag and message to show.
type surfaceFunc = func(line string) (level, msg string, ok bool)

func noSurface(string) (string, string, bool) { return "", "", false }

// surfaceHostAgentLog turns one slog JSON record into a console line.
//
// The host-agent logs JSON to stdout, which is right for a journald-managed
// production process and unreadable in a developer terminal. Only WARN and
// above are surfaced: the INFO stream is mostly the reconciler describing
// work it is doing correctly.
func surfaceHostAgentLog(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.HasPrefix(trimmed, "{") {
		return "", "", false
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		return "", "", false
	}
	level := strings.ToUpper(strings.TrimSpace(fmt.Sprint(rec["level"])))
	switch level {
	case "WARN", "WARNING", "ERROR", "FATAL":
	default:
		return "", "", false
	}
	msg, _ := rec["msg"].(string)
	if strings.TrimSpace(msg) == "" {
		msg = "<no message>"
	}
	if fields := slogFields(rec); fields != "" {
		msg += "  " + fields
	}
	return level, clip(msg, 300), true
}

// slogFields renders the record's attributes, minus the envelope keys, as a
// compact `k=v` list in a stable order.
func slogFields(rec map[string]any) string {
	keys := make([]string, 0, len(rec))
	for k := range rec {
		switch k {
		case "time", "level", "msg", "source":
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		v := strings.TrimSpace(fmt.Sprint(rec[k]))
		if v == "" || v == "<nil>" {
			continue
		}
		if strings.ContainsAny(v, " \t\"") {
			v = fmt.Sprintf("%q", v)
		}
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, " ")
}

// surfaceDashboardLog keeps the dashboard dev server's failures and drops its
// banner. Vite has no machine-readable output, so this is a keyword filter,
// and it errs toward showing anything that mentions a problem.
func surfaceDashboardLog(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return "", "", false
	}
	lower := strings.ToLower(trimmed)
	switch {
	case strings.Contains(lower, "error"),
		strings.Contains(lower, "failed"),
		strings.Contains(lower, "cannot find"),
		strings.Contains(lower, "eaddrinuse"),
		strings.Contains(lower, "internal server error"):
		return "ERROR", "dashboard: " + clip(trimmed, 300), true
	}
	return "", "", false
}

// streamTee mirrors raw subprocess output to the dev log and decides what the
// console sees. It also remembers the tail of the stream so a failed step can
// print the lines that explain the failure.
type streamTee struct {
	mu      sync.Mutex
	console *devConsole
	surface surfaceFunc
	tail    []string
	buf     []byte
}

// Write mirrors p to the log always. A verbose console gets the same bytes; a
// quiet one gets only the lines its surface function picks, so line
// boundaries have to be tracked here rather than trusted from the producer.
func (t *streamTee) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	_, _ = t.console.log.Write(p)

	if t.console.verbose {
		_, _ = t.console.out.Write(p)
		return len(p), nil
	}

	t.buf = append(t.buf, p...)
	for {
		idx := bytes.IndexByte(t.buf, '\n')
		if idx < 0 {
			if len(t.buf) > devLineBufferLimit {
				t.consider(string(t.buf))
				t.buf = t.buf[:0]
			}
			return len(p), nil
		}
		line := string(t.buf[:idx])
		t.buf = append(t.buf[:0], t.buf[idx+1:]...)
		t.consider(line)
	}
}

// consider records a completed line in the tail and surfaces it if the
// stream's filter says it belongs on the console.
func (t *streamTee) consider(line string) {
	t.remember(line)
	if t.surface == nil {
		return
	}
	if level, msg, ok := t.surface(line); ok {
		t.console.Emit(level, msg)
	}
}

// remember keeps a bounded tail of raw lines.
func (t *streamTee) remember(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	t.tail = append(t.tail, clip(line, devTailLineChar))
	if len(t.tail) > devTailLines {
		t.tail = append(t.tail[:0], t.tail[len(t.tail)-devTailLines:]...)
	}
}

// Tail returns the last few raw lines, oldest first. This is what turns "build
// failed" into something actionable without opening the log file.
func (t *streamTee) Tail(n int) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if n <= 0 || len(t.tail) == 0 {
		return nil
	}
	if n > len(t.tail) {
		n = len(t.tail)
	}
	out := make([]string, n)
	copy(out, t.tail[len(t.tail)-n:])
	return out
}

// Close flushes any trailing partial line to the console filter and closes the
// dev log.
func (c *devConsole) Close() error {
	if c.logFile != nil {
		err := c.logFile.Close()
		c.logFile = nil
		c.log = io.Discard
		return err
	}
	return nil
}

// padRight pads s with spaces to w columns (measured in runes, so the
// multi-byte marks do not shift the column).
func padRight(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// padLeft pads s on the left to w columns.
func padLeft(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return strings.Repeat(" ", w-n) + s
	}
	return s
}

// clip truncates s to n runes, marking the cut.
func clip(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 3 {
		return string(r[:n])
	}
	return string(r[:n-3]) + "..."
}

// fallbackStr returns s when non-empty, otherwise the fallback.
func fallbackStr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// joinTail flattens a captured tail back into one block.
func joinTail(lines []string) string {
	return strings.Join(lines, "\n")
}

// detailLines splits a captured failure detail into at most n trimmed lines.
func detailLines(detail string, n int) []string {
	if strings.TrimSpace(detail) == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(detail, "\n"), "\n")
	out := make([]string, 0, n)
	for _, l := range lines {
		l = strings.TrimRight(l, " \t")
		if strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, clip(l, devTailLineChar))
		if len(out) == n {
			break
		}
	}
	return out
}
