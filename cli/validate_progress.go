// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Live progress for the integration tier.
//
// The tier's quiet-console rule -- one line per phase -- is right for a phase
// that takes seconds and wrong for the two that do not. `wait-for-convergence`
// runs for minutes while the orchestrator brings Traefik, Authentik, and the
// LDAP outpost up, and the test binary runs for minutes more. With the whole
// phase captured, the console sits silent through both and then prints a
// verdict. In GitHub Actions that silence is not merely unhelpful: a step that
// writes nothing for 10 minutes is killed by the runner's idle timeout, so a
// legitimately slow convergence looks like a hung job and the evidence of
// what it was waiting on never appears.
//
// These filters fix that without giving up the quiet console. They sit between
// a subprocess and the terminal, and print only the lines that are a state
// change: one per test case as it starts and finishes, one per node as it
// moves through a lifecycle phase. Every raw byte still goes to the phase's
// capture buffer and thence to `.bloud/logs/validate-integration.log`, so
// nothing is lost and a failure still replays in full.
//
// There is deliberately no heartbeat. Both producers emit real events at a
// rate that keeps the console visibly alive; a synthetic "still working" line
// would be noise on top of the signal.
const (
	// progressIndent nests a live line under the phase line it belongs to,
	// which is printed at the two-space phase indent.
	progressIndent = "      "

	// progressNameWidth pads the node or test-case field so the phase text
	// forms a column. Longer names are truncated with an ellipsis rather than
	// pushing the column around.
	progressNameWidth = 30

	// progressLineLimit bounds the partial-line buffer. A producer that never
	// emits a newline must not grow the buffer without end; the overflow is
	// flushed as one long line instead.
	progressLineLimit = 1 << 16
)

// lineWriter turns a byte stream into whole-line callbacks. Subprocess output
// arrives in arbitrary chunks -- a JSON log record can straddle two reads --
// so a filter cannot work per Write call. The mutex is held across the whole
// callback because a phase's stdout and stderr both feed the console, and a
// torn interleaving mid-line is exactly the mess this exists to prevent.
type lineWriter struct {
	mu      sync.Mutex
	buf     []byte
	out     io.Writer
	handle  func(line string)
	paintFn func(s, color string) string
}

// Write implements io.Writer. It never fails: a console that cannot be
// written to is not the subprocess's problem, and returning an error here
// would kill a running test run over a terminal hiccup.
func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.handle(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > progressLineLimit {
		w.handle(w.flushAll())
	}
	return len(p), nil
}

// Flush emits any buffered partial line, so a final line with no trailing
// newline still reaches the filter.
func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.handle(w.flushAll())
	}
}

// flushAll returns the buffer contents and empties it. Callers hold the lock.
func (w *lineWriter) flushAll() string {
	s := string(w.buf)
	w.buf = w.buf[:0]
	return s
}

// emit writes one rendered progress line: the mark, the padded name field,
// and the detail beside it. A detail that spans several lines (a logged error
// string) keeps its first line on the row and gets the rest indented
// underneath, so the column above it survives.
func (w *lineWriter) emit(mark, color, name, detail string) {
	head := progressIndent + w.paintFn(mark, color) + " " + padField(name, progressNameWidth)
	if detail == "" {
		_, _ = fmt.Fprintln(w.out, strings.TrimRight(head, " "))
		return
	}
	lines := strings.Split(strings.TrimRight(detail, "\n"), "\n")
	_, _ = fmt.Fprintf(w.out, "%s\n", strings.TrimRight(head+"  "+lines[0], " "))
	for _, l := range lines[1:] {
		_, _ = fmt.Fprintf(w.out, "%s\n", strings.TrimRight(progressIndent+"    "+l, " "))
	}
}

// padField right-pads s to width. It never truncates: the name field is a
// test case's identity, and cutting one off to keep a column tidy would hide
// exactly which case failed. A name longer than the width simply pushes its
// own detail along.
func padField(s string, width int) string {
	if utf8.RuneCountInString(s) >= width {
		return s
	}
	return fmt.Sprintf("%-*s", width, s)
}

// testProgress renders `go test -v` output as one console line per test
// case. The runner's own chatter -- coverage noise, per-test logging from
// passing tests, the final `PASS` -- is dropped: the verdict per case is the
// signal, and the full transcript is one file away.
type testProgress struct {
	w        *lineWriter
	started  int
	passed   int
	failed   int
	skipped  int
	finished bool
}

func newTestProgress(out io.Writer, color bool) *testProgress {
	p := &testProgress{}
	p.w = &lineWriter{out: out, paintFn: paintFor(color)}
	p.w.handle = p.line
	return p
}

// Write implements io.Writer for the test stream.
func (p *testProgress) Write(b []byte) (int, error) { return p.w.Write(b) }

// Finish flushes a trailing partial line and writes the tally. The caller
// must call it once the stream ends, or the last case never appears.
func (p *testProgress) Finish() {
	p.w.Flush()
	p.summary()
}

// line handles one line of `go test -v` output.
func (p *testProgress) line(raw string) {
	line := strings.TrimRight(raw, "\r")
	trimmed := strings.TrimLeft(line, " \t")
	depth := (len(line) - len(trimmed)) / 4
	if depth < 0 {
		depth = 0
	}

	switch {
	case strings.HasPrefix(trimmed, "=== RUN"):
		name := strings.TrimSpace(trimmed[len("=== RUN"):])
		if name == "" {
			return
		}
		p.started++
		p.w.emit("▶", colorCyan, indentName(name, depth), "")
	case strings.HasPrefix(trimmed, "--- SKIP:"):
		name, dur := splitCaseResult(trimmed, "SKIP")
		p.skipped++
		p.w.emit("⊘", colorYellow, indentName(name, depth), dur)
	case strings.HasPrefix(trimmed, "--- FAIL:"):
		name, dur := splitCaseResult(trimmed, "FAIL")
		p.failed++
		p.w.emit("✗", colorRed, indentName(name, depth), dur)
	case strings.HasPrefix(trimmed, "--- PASS:"):
		name, dur := splitCaseResult(trimmed, "PASS")
		p.passed++
		p.w.emit("✓", colorGreen, indentName(name, depth), dur)
	}
}

// summary writes the tally once the stream ends, so a filtered run still says
// how many cases it saw. A run that produced no test cases at all -- a bad
// -test.run regex that matched nothing, say -- is worth saying out loud,
// because otherwise it looks identical to a suite that passed.
func (p *testProgress) summary() {
	if p.finished {
		return
	}
	p.finished = true
	if p.started == 0 && p.passed == 0 && p.failed == 0 && p.skipped == 0 {
		p.w.emit("!", colorYellow, "no test cases ran", "the -test.run regex matched nothing")
		return
	}
	p.w.emit("=", colorDim, fmt.Sprintf("%d passed, %d failed, %d skipped",
		p.passed, p.failed, p.skipped), "")
}

// splitCaseResult pulls the case name and its duration out of a
// `--- PASS: Name (1.23s)` line.
func splitCaseResult(trimmed, verb string) (name, dur string) {
	prefix := "--- " + verb + ": "
	rest := strings.TrimPrefix(trimmed, prefix)
	if i := strings.LastIndex(rest, " ("); i >= 0 && strings.HasSuffix(rest, ")") {
		return rest[:i], rest[i+2 : len(rest)-1]
	}
	return rest, ""
}

// indentName nests a subtest name under its parent.
func indentName(name string, depth int) string {
	if depth == 0 {
		return name
	}
	return strings.Repeat("  ", depth) + name
}

// agentProgress renders the host-agent's structured log as convergence
// progress. The agent logs one JSON object per line, and the records that
// matter during bring-up are the orchestrator's: which convergence step it
// is in, and which lifecycle phase each node is moving through. Everything
// else at INFO is dropped; WARN and ERROR always print, because a warning
// during bring-up is the thing an operator needs to see while it is still
// cheap to cancel the run.
type agentProgress struct {
	w     *lineWriter
	color bool
}

func newAgentProgress(out io.Writer, color bool) *agentProgress {
	p := &agentProgress{color: color}
	p.w = &lineWriter{out: out, paintFn: paintFor(color)}
	p.w.handle = p.line
	return p
}

// Write implements io.Writer for the agent's log stream.
func (p *agentProgress) Write(b []byte) (int, error) { return p.w.Write(b) }

// Finish flushes a trailing partial line. The agent stream has no closing
// summary: the phase line underneath it already carries the verdict.
func (p *agentProgress) Finish() { p.w.Flush() }

// line handles one line of the agent's log.
func (p *agentProgress) line(raw string) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return
	}
	if !strings.HasPrefix(line, "{") {
		p.emitPlain(line)
		return
	}

	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		p.emitPlain(line)
		return
	}

	msg, _ := rec["msg"].(string)
	level, _ := rec["level"].(string)
	if level == "WARN" || level == "ERROR" {
		p.emitWarn(level, msg, rec)
		return
	}
	if label, ok := agentConvergenceLabel(msg, rec); ok {
		p.w.emit("·", colorDim, label.name, label.phase)
	}
}

type agentLabel struct{ name, phase string }

// agentConvergenceLabel maps an agent log message to the node-and-phase pair
// the console shows. Messages outside the table are not progress: the agent
// logs steadily, and printing all of it would bury the transitions this is
// here to surface.
func agentConvergenceLabel(msg string, rec map[string]any) (agentLabel, bool) {
	app, _ := rec["app"].(string)

	switch {
	case msg == "convergence step":
		step, _ := rec["step"].(string)
		return agentLabel{"convergence", step}, true
	case msg == "convergence pass complete":
		n, _ := rec["apps"].(string)
		if n == "" {
			n = fmt.Sprint(rec["apps"])
		}
		return agentLabel{"convergence", "pass complete (" + n + " apps)"}, true
	case msg == "dispatching full lifecycle":
		return agentLabel{app, "starting"}, true
	case msg == "marking node RUNNING after route generation":
		return agentLabel{app, "RUNNING"}, true
	case strings.HasPrefix(msg, "lifecycle phase: "):
		phase := strings.TrimPrefix(msg, "lifecycle phase: ")
		// The `... complete` records restate progress the next start event
		// already carries, so they would double every line.
		if strings.HasSuffix(phase, " complete") {
			return agentLabel{}, false
		}
		return agentLabel{app, phase}, true
	default:
		return agentLabel{}, false
	}
}

// emitWarn prints a WARN or ERROR record with its fields, since a bring-up
// warning is only actionable with the values that triggered it.
func (p *agentProgress) emitWarn(level, msg string, rec map[string]any) {
	name := level + "  " + msg
	detail := formatRecordFields(rec)
	p.w.emit("!", colorYellow, name, detail)
}

// formatRecordFields renders the record's non-envelope fields as `k=v`, in
// sorted order so the same warning reads the same way twice.
func formatRecordFields(rec map[string]any) string {
	keys := make([]string, 0, len(rec))
	for k := range rec {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		keys = append(keys, k)
	}
	sortStrings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+fmt.Sprint(rec[k]))
	}
	return strings.Join(parts, " ")
}

// podmanEventLine matches the libpod event stream that the container library
// writes straight to the agent's stdout: a timestamp, then `container <event>
// <64-hex-id> (labels...)`. It is not the agent's judgment about anything,
// and during bring-up it is dozens of lines per node, each carrying every
// container label -- enough to bury the lifecycle transitions this filter
// exists to surface. The raw bytes still land in the phase transcript, so
// the evidence is one file away; it just does not occupy the console.
var podmanEventLine = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d+ .* container \w+ [0-9a-f]{64}( |$)`)

// emitPlain prints a line the agent wrote that is not a JSON record. The
// agent's own stdout can carry non-JSON (a panic trace, a child process
// writing to the same fd), and dropping that would hide the one line that
// explains a crash. Podman's event stream is the exception, and the reason
// it is called out separately rather than just not matching.
func (p *agentProgress) emitPlain(raw string) {
	if podmanEventLine.MatchString(raw) {
		return
	}
	p.w.emit("!", colorYellow, strings.TrimSpace(raw), "")
}

// paintFor returns a painter that wraps in color only when the console has
// one, matching the dev console's rule so piped output stays plain text.
func paintFor(color bool) func(s, c string) string {
	return func(s, c string) string {
		if !color || c == "" {
			return s
		}
		return c + s + colorReset
	}
}

// sortStrings is a tiny insertion sort so this file does not pull in "sort"
// for one call site.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
