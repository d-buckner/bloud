// SPDX-License-Identifier: AGPL-3.0-only

package hermeswebui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/managedfile"
)

const (
	// configFileName is the Hermes agent's own config file, at
	// $HERMES_HOME/config.yaml. $HERMES_HOME is /home/hermeswebui/.hermes,
	// mounted from {{appDataDir}}/data, so on the host this is
	// <DataPath>/data/config.yaml.
	configFileName = "config.yaml"

	// containerHome is $HERMES_HOME inside the image. A read that goes
	// through the container addresses the file by this path, not the host
	// path, which does not exist in there.
	containerHome = "/home/hermeswebui/.hermes"

	// inferenceProvidersKey is the config section holding named provider
	// entries, and inferenceProviderKey is the entry Bloud owns.
	inferenceProvidersKey = "providers"
	inferenceProviderKey  = "bloud"

	// inferenceProviderSlug is the identity that *selects* the entry in
	// model.provider. Same convention as apps/hermes, which writes the same
	// file format for the same agent.
	inferenceProviderSlug = "custom:" + inferenceProviderKey

	// inferenceAPIMode names the wire protocol so the agent does not have
	// to guess it from the endpoint shape.
	inferenceAPIMode = "chat_completions"
)

// convergeConfig makes <dataDir>/data/config.yaml carry (or not carry) the
// Bloud inference provider, and reports whether the file changed.
//
// The edit is compared as a parsed document rather than as bytes: a
// re-marshal of an unchanged document yields the same document, so only a
// real difference in the managed keys writes anything. Every key the agent
// itself owns, including a model the operator picked in the UI, is carried
// through untouched.
//
// allowExec decides what a refused host write means. Before the container
// has ever run, the host owns the tree and writes directly; passing false
// there keeps a first install from trying to exec into a container that does
// not exist yet. Once the agent has run it owns the file, and the write has
// to go through the running container, which is what passing true buys.
func (c *Configurator) convergeConfig(ctx context.Context, dataDir string, state *configurator.AppState, allowExec bool) (bool, error) {
	cfgPath := filepath.Join(dataDir, "data", configFileName)

	existing, err := c.readConfig(ctx, cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("reading %s: %w", cfgPath, err)
	}

	doc, err := parseConfig(existing)
	if err != nil {
		return false, fmt.Errorf("parsing %s: %w", cfgPath, err)
	}
	base, err := yaml.Marshal(doc)
	if err != nil {
		return false, fmt.Errorf("serializing %s: %w", cfgPath, err)
	}

	binding, hasInference := inferenceBinding(state)
	if hasInference {
		applyInference(doc, binding)
	} else {
		stripInference(doc)
	}

	want, err := yaml.Marshal(doc)
	if err != nil {
		return false, fmt.Errorf("serializing %s: %w", cfgPath, err)
	}
	if bytes.Equal(base, want) {
		return false, nil
	}

	changed, err := c.writeConfig(ctx, cfgPath, want, allowExec)
	if err != nil {
		return false, err
	}
	if changed {
		c.logger.Info("updated the agent config", "path", cfgPath, "inference", hasInference)
	}
	return changed, nil
}

// writeConfig puts the converged document in place through the host if the
// host can, and through the running container if it cannot.
//
// The host path is a temp file plus rename inside the config's directory,
// which needs write permission on the directory rather than on the file.
// That permission exists exactly until the agent's first start: the image
// chowns $HERMES_HOME to its runtime user, and under rootless podman that
// user is a host subuid the host agent is neither owner nor group of. From
// then on the only writer with a right to the file is the container itself,
// so the write goes there.
func (c *Configurator) writeConfig(ctx context.Context, cfgPath string, want []byte, allowExec bool) (bool, error) {
	changed, err := managedfile.Write(cfgPath, want, managedfile.ModeSharedConfig)
	if err == nil {
		return changed, nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return false, fmt.Errorf("writing %s: %w", cfgPath, err)
	}
	if !allowExec {
		// Not a failure: the app is not up yet, and PostStart converges the
		// same document with the container as the writer.
		c.logger.Info("the container owns the agent config directory; the write waits for the app to be running",
			"path", cfgPath)
		return false, nil
	}
	if c.exec == nil {
		return false, fmt.Errorf("writing %s: %w%s", cfgPath, err, containerWriteUnavailable)
	}
	if execErr := c.writeConfigViaExec(ctx, want); execErr != nil {
		return false, fmt.Errorf("writing %s inside %s: %w", configFileName, nodeName, execErr)
	}
	c.logger.Info("wrote the agent config through the running container", "path", cfgPath)
	return true, nil
}

// writeConfigViaExec writes the document as a process inside the container.
//
// Deps.Exec has no stdin channel, so the content travels inside the command
// line, base64-encoded: the encoding keeps every byte of the YAML out of the
// shell's way, and the alphabet cannot contain a quote, so the single quotes
// around it need no escaping.
//
// The umask and the trailing chmod are both load-bearing, and they are there
// for the same reason: the exec'd process runs as the image's configured
// user, which in this image is root, while the app itself runs as
// hermeswebui after the init script drops privileges. A file the exec side
// leaves 0600 is unreadable to the server that has to read it, which is the
// same cross-uid problem managedfile.ModeSharedConfig exists to solve on the
// host side, arriving again on the other side of the mount.
//
// The umask alone is not enough, because a shell redirection does not
// change the mode of a file that already exists: it only truncates. The
// explicit chmod is what makes the write deterministic whatever the file's
// prior mode was, instead of leaving the result dependent on who created the
// file and when.
func (c *Configurator) writeConfigViaExec(ctx context.Context, want []byte) error {
	encoded := base64.StdEncoding.EncodeToString(want)
	cmd := []string{"sh", "-c", fmt.Sprintf(
		"umask 022; printf %%s '%s' | base64 -d > %s; chmod %o %s",
		encoded, containerConfigPath, int(managedfile.ModeSharedConfig), containerConfigPath)}
	_, err := c.exec(ctx, nodeName, nil, cmd)
	return err
}

// containerWriteUnavailable says why a host write can be refused on a file
// the host agent wrote itself, because "permission denied" there otherwise
// reads like a Bloud bug rather than the consequence of the app reclaiming
// its own home directory.
const containerWriteUnavailable = " (the agent owns $HERMES_HOME once it has started, and under rootless podman " +
	"that is a host uid the host agent is not; the config can only be written through the running container - " +
	"see apps/hermes-webui/INTEGRATION.md)"

// readConfig reads the agent's config file, falling back to the container
// when the host-side read is refused.
//
// Once the agent has rewritten its own config (the user changed a setting in
// the UI), the file is owned by the container's uid, which under rootless
// podman is a host uid in the subuid range and mode 0600. The host agent
// wrote the file originally and can no longer read it. HERMES_HOME_MODE
// makes the *directory* writable, which is the write path; this is the read
// path, and it goes through the container, which reads its own file without
// complaint.
//
// The bytes come back base64-encoded on purpose. Deps.Exec is a combined
// stdout+stderr channel, so a runtime warning would otherwise land inside
// the YAML. Encoded, contamination fails the decode instead of being merged
// and written back over the real config.
func (c *Configurator) readConfig(ctx context.Context, cfgPath string) ([]byte, error) {
	raw, err := os.ReadFile(cfgPath)
	if err == nil {
		return raw, nil
	}
	if !errors.Is(err, fs.ErrPermission) {
		return nil, err
	}
	if c.exec == nil {
		return nil, fmt.Errorf("%w%s", err, containerReadUnavailable)
	}

	out, execErr := c.exec(ctx, nodeName, nil, []string{"base64", containerHome + "/" + configFileName})
	if execErr != nil {
		return nil, fmt.Errorf("%w%s (reading it inside %s failed too: %v)",
			err, containerReadUnavailable, nodeName, execErr)
	}
	decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if decErr != nil {
		return nil, fmt.Errorf("decoding %s read from %s: %w", configFileName, nodeName, decErr)
	}
	c.logger.Info("read the agent config through the container: the host agent cannot read a file the container owns",
		"path", cfgPath)
	return decoded, nil
}

// containerConfigPath is the agent config file as the container addresses it.
const containerConfigPath = containerHome + "/" + configFileName

// containerReadUnavailable names the mechanism behind a refused host read,
// because "permission denied" on a file the agent itself wrote reads like a
// Bloud bug rather than the consequence of the app reclaiming its config.
const containerReadUnavailable = " (the agent chowns its own config.yaml to its container runtime user, which under " +
	"rootless podman is a host uid the agent is not; the file can only be read through the running container - " +
	"see apps/hermes-webui/INTEGRATION.md)"

// parseConfig decodes the agent config into a generic map. A missing or
// empty file is an empty document: the agent fills its own defaults at
// runtime, and so does this merge.
func parseConfig(raw []byte) (map[string]any, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(raw)) == 0 {
		return doc, nil
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
}

// inferenceBinding returns the instance inference binding, if there is a
// usable one. An empty endpoint is "not configured", not "use an empty URL":
// the agent would store a provider that cannot be dialed.
func inferenceBinding(state *configurator.AppState) (configurator.InferenceBinding, bool) {
	if state == nil || len(state.Integrations.Inference) == 0 {
		return configurator.InferenceBinding{}, false
	}
	b := state.Integrations.Inference[0]
	if b.Endpoint == "" {
		return configurator.InferenceBinding{}, false
	}
	return b, true
}

// applyInference registers the Bloud provider block, and adopts the
// instance default model only where the agent has chosen none.
//
// The block is written whenever a binding exists, even if the operator has
// pointed the agent somewhere else: keeping it registered means switching
// back in the UI does not require re-typing an endpoint. The model selection
// is the part that respects an existing choice, because silently replacing a
// model an operator picked is the failure mode that makes a managed default
// unwelcome.
func applyInference(doc map[string]any, b configurator.InferenceBinding) {
	providers := mapAt(doc, inferenceProvidersKey)
	p := mapAt(providers, inferenceProviderKey)
	p["name"] = "Bloud"
	p["api_mode"] = inferenceAPIMode
	p["base_url"] = b.Endpoint
	if b.APIKey != "" {
		p["api_key"] = b.APIKey
	} else {
		delete(p, "api_key")
	}
	if b.DefaultModel != "" {
		p["default_model"] = b.DefaultModel
	}
	// Ask the agent to keep the model list current from the endpoint rather
	// than trusting a snapshot, so a model added upstream is reachable
	// without a Bloud change.
	p["discover_models"] = true

	adoptDefaultModel(doc, b.DefaultModel)
}

// adoptDefaultModel sets the active model to the Bloud default only when
// the agent has no selection of its own. An existing model.provider is left
// exactly as the operator set it.
func adoptDefaultModel(doc map[string]any, defaultModel string) {
	if defaultModel == "" {
		return
	}
	model := mapAt(doc, "model")
	if existing, ok := model["provider"].(string); ok && existing != "" {
		return
	}
	model["provider"] = inferenceProviderSlug
	model["model"] = defaultModel
}

// stripInference removes the Bloud-managed provider block, and the model
// selection only when that selection is the one Bloud wrote. An
// operator-chosen model pointing elsewhere survives, so removing Bloud's
// inference cannot break a configuration someone built by hand.
func stripInference(doc map[string]any) {
	if providers, ok := doc[inferenceProvidersKey].(map[string]any); ok {
		delete(providers, inferenceProviderKey)
		if len(providers) == 0 {
			delete(doc, inferenceProvidersKey)
		}
	}

	model, ok := doc["model"].(map[string]any)
	if !ok {
		return
	}
	active, _ := model["model"].(string)
	provider, _ := model["provider"].(string)
	if provider != inferenceProviderSlug || active == "" {
		return
	}
	delete(model, "model")
	delete(model, "provider")
	if len(model) == 0 {
		delete(doc, "model")
	}
}

// mapAt returns the map at key, creating it if absent or if what is there is
// not a map.
func mapAt(doc map[string]any, key string) map[string]any {
	if m, ok := doc[key].(map[string]any); ok {
		return m
	}
	m := map[string]any{}
	doc[key] = m
	return m
}
