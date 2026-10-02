// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
// The transcript reports through fmt.Printf, so this is the only way to see
// the console it produced.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = orig
	out := <-done
	_ = r.Close()
	return out
}

func newTestTranscript(t *testing.T, flags validateFlags) *integrationTranscript {
	t.Helper()
	return newIntegrationTranscript(t.TempDir(), flags)
}

func chattyStep(lines int, err error) func(io.Writer) (int, error) {
	return func(out io.Writer) (int, error) {
		for i := 0; i < lines; i++ {
			_, _ = io.WriteString(out, "chunk of toolchain noise\n")
		}
		if err != nil {
			return 1, err
		}
		return 0, nil
	}
}

func readTierLog(t *testing.T, tr *integrationTranscript) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(tr.root, ".bloud", "logs", "validate-integration.log"))
	if err != nil {
		t.Fatalf("read tier log: %v", err)
	}
	return string(data)
}

// TestIntegrationTranscriptPassesWithOneLinePerPhase is the whole point of
// the change: a passing phase contributes one verdict line to the terminal
// and none of its own chatter.
func TestIntegrationTranscriptPassesWithOneLinePerPhase(t *testing.T) {
	tr := newTestTranscript(t, validateFlags{})

	out := captureStdout(t, func() {
		if _, err := tr.phase(bringUpPhase("build-host-agent", "go build"), chattyStep(40, nil)); err != nil {
			t.Errorf("unexpected phase error: %v", err)
		}
	})

	if strings.Count(out, "\n") != 1 {
		t.Errorf("a passing phase should print exactly one line, got %d:\n%s", strings.Count(out, "\n"), out)
	}
	if !strings.Contains(out, "build-host-agent") {
		t.Errorf("the verdict line should name the phase, got:\n%s", out)
	}
	if strings.Contains(out, "chunk of toolchain noise") {
		t.Errorf("a passing phase's output should stay off the console:\n%s", out)
	}
	captureStdout(t, tr.finish)
	if got := readTierLog(t, tr); !strings.Contains(got, "chunk of toolchain noise") {
		t.Errorf("the tier log should carry the evidence, got:\n%s", got)
	}
}

// TestIntegrationTranscriptReplaysAFailingPhaseOutput: the moment a phase
// fails, its output is the message, so it goes on the console, indented
// under a header that names the phase.
func TestIntegrationTranscriptReplaysAFailingPhaseOutput(t *testing.T) {
	tr := newTestTranscript(t, validateFlags{})

	out := captureStdout(t, func() {
		_, err := tr.phase(bringUpPhase("build-host-agent", "go build"),
			chattyStep(3, errors.New("compile failed")))
		if err == nil {
			t.Error("a failing phase must return an error")
		}
	})

	if !strings.Contains(out, "build-host-agent failed") {
		t.Errorf("missing the failure header:\n%s", out)
	}
	if strings.Count(out, "chunk of toolchain noise") != 3 {
		t.Errorf("the failing phase's output should be replayed in full:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "chunk of toolchain noise") && !strings.HasPrefix(line, "    ") {
			t.Errorf("replayed output should be indented, got %q", line)
		}
	}
}

// TestIntegrationTranscriptJSONModeKeepsStdoutClean: the ledger owns stdout
// in JSON mode, so nothing from a phase reaches the terminal, but the log
// file still gets the bytes.
func TestIntegrationTranscriptJSONModeKeepsStdoutClean(t *testing.T) {
	tr := newTestTranscript(t, validateFlags{json: true})

	out := captureStdout(t, func() {
		_, _ = tr.phase(bringUpPhase("build-host-agent", "go build"), chattyStep(5, nil))
	})

	if out != "" {
		t.Errorf("JSON mode should print nothing, got:\n%s", out)
	}
	captureStdout(t, tr.finish)
	if got := readTierLog(t, tr); !strings.Contains(got, "chunk of toolchain noise") {
		t.Errorf("JSON mode should still write the log, got:\n%s", got)
	}
}

// TestIntegrationTranscriptVerboseStreamsLive is the escape hatch: --verbose
// puts the raw output on the console as it happens.
func TestIntegrationTranscriptVerboseStreamsLive(t *testing.T) {
	tr := newTestTranscript(t, validateFlags{verbose: true})

	out := captureStdout(t, func() {
		_, _ = tr.phase(bringUpPhase("build-host-agent", "go build"), chattyStep(4, nil))
	})

	if strings.Count(out, "chunk of toolchain noise") != 4 {
		t.Errorf("--verbose should stream the phase output live:\n%s", out)
	}
}

// TestIntegrationTranscriptFinishSummarizesEveryPhase checks the tier ends
// with the same summary shape the fast tier prints, counting bring-up phases
// and test phases together.
func TestIntegrationTranscriptFinishSummarizesEveryPhase(t *testing.T) {
	tr := newTestTranscript(t, validateFlags{})
	_, _ = tr.phase(bringUpPhase("provision-host", "ensure the runtime"), chattyStep(1, nil))
	_, _ = tr.phase(manifestCommand{ID: "e2e-host-agent", Cwd: "/tmp", Run: "./bloud-integration.test"}, chattyStep(1, nil))

	out := captureStdout(t, tr.finish)

	if !strings.Contains(out, "integration tier passed, 2/2") {
		t.Errorf("missing the tier summary line:\n%s", out)
	}
	if !strings.Contains(out, "validate-integration.log") {
		t.Errorf("the summary should point at the full log:\n%s", out)
	}
}

func TestIntegrationTranscriptFinishReportsFailure(t *testing.T) {
	tr := newTestTranscript(t, validateFlags{})
	_, _ = tr.phase(bringUpPhase("provision-host", "ensure the runtime"), chattyStep(1, nil))
	_, _ = tr.phase(bringUpPhase("wait-for-convergence", "poll the health endpoint"), chattyStep(1, errors.New("never healthy")))

	out := captureStdout(t, tr.finish)

	if !strings.Contains(out, "integration tier failed, 1/2") {
		t.Errorf("missing the failed tier summary:\n%s", out)
	}
	if !strings.Contains(out, "wait-for-convergence") {
		t.Errorf("the summary should name what failed:\n%s", out)
	}
}

// TestIntegrationArtifactsShipNoFrontendBuild pins the second half of the
// change: the validation runtime is deployed without a dashboard bundle.
// host-agent stats web/build and serves its documented missing-build
// fallback page instead (invariant 11), and the fast tier's `web-build`
// command is what gates the real bundle.
func TestIntegrationArtifactsShipNoFrontendBuild(t *testing.T) {
	arts := integrationArtifacts("/repo", "/tmp/build", "/rt")

	var destinations []string
	for _, a := range arts {
		destinations = append(destinations, a.to)
		if strings.Contains(a.to, "web/build") || strings.Contains(a.from, "web/build") {
			t.Errorf("the integration tier must not deploy the frontend bundle: %s -> %s", a.from, a.to)
		}
	}
	for _, want := range []string{"/rt/host-agent/host-agent", "/rt/apps", "/rt/bin/bloud-integration.test"} {
		found := false
		for _, got := range destinations {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q among the deployed artifacts, got %v", want, destinations)
		}
	}
}

// TestIntegrationTierNeverBuildsTheFrontend reads the tier's own source and
// asserts no frontend build call survives in it. Comments are stripped first:
// prose about the frontend is welcome, an `npm run build` is not.
func TestIntegrationTierNeverBuildsTheFrontend(t *testing.T) {
	src, err := os.ReadFile("validate_integration.go")
	if err != nil {
		t.Fatalf("read tier source: %v", err)
	}
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		if strings.Contains(line, "npm") || strings.Contains(line, "host-agent-web") {
			t.Errorf("the integration tier should not build the frontend, found: %s", line)
		}
	}
}
