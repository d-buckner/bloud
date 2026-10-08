// SPDX-License-Identifier: AGPL-3.0-only

package inference

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/store"
)

// memSettings is an in-memory SettingsStoreInterface so the migration can be
// driven without a database.
type memSettings struct {
	values map[string]string
	err    error
}

func newMemSettings() *memSettings { return &memSettings{values: map[string]string{}} }

func (f *memSettings) Get(key string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.values[key], nil
}

func (f *memSettings) Set(key, value string) error {
	if f.err != nil {
		return f.err
	}
	f.values[key] = value
	return nil
}

// memSecrets is an in-memory AppSecretsProvider keyed the way the real manager
// keys it: one scope per provider record, one name per contract.
type memSecrets struct {
	values map[string]string
}

func newMemSecrets() *memSecrets { return &memSecrets{values: map[string]string{}} }

func (f *memSecrets) key(app, name string) string { return app + "/" + name }

func (f *memSecrets) GenerateAppAdminPassword(string) (string, error) { return "", nil }
func (f *memSecrets) GetAppSecret(app, name string) string {
	return f.values[f.key(app, name)]
}
func (f *memSecrets) SetAppSecret(app, name, value string) error {
	f.values[f.key(app, name)] = value
	return nil
}
func (f *memSecrets) SetAppContractValue(string, string, string, string) error { return nil }
func (f *memSecrets) GetAppContractValue(string, string, string) string        { return "" }
func (f *memSecrets) DeleteAppSecrets(app string) error {
	for k := range f.values {
		if len(k) >= len(app) && k[:len(app)+1] == app+"/" {
			delete(f.values, k)
		}
	}
	return nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// memExternal is an in-memory external app registry keyed by ID.
type memExternal struct {
	records map[string]*store.ExternalApp
}

func newMemExternal() *memExternal { return &memExternal{records: map[string]*store.ExternalApp{}} }

func (m *memExternal) GetAll() ([]*store.ExternalApp, error) {
	var out []*store.ExternalApp
	for _, r := range m.records {
		out = append(out, r)
	}
	return out, nil
}
func (m *memExternal) Get(id string) (*store.ExternalApp, error) { return m.records[id], nil }
func (m *memExternal) FindBySource(string) (*store.ExternalApp, error) {
	return nil, errors.New("not needed")
}
func (m *memExternal) FindAllBySource(source string) ([]*store.ExternalApp, error) {
	var out []*store.ExternalApp
	for _, r := range m.records {
		if r.Source == source {
			out = append(out, r)
		}
	}
	return out, nil
}
func (m *memExternal) Upsert(app *store.ExternalApp) error {
	m.records[app.ID] = app
	return nil
}
func (m *memExternal) Delete(id string) error { delete(m.records, id); return nil }
func (m *memExternal) SetOnChange(func())     {}

// The migration is the whole point of the change: an instance that already has
// AI configured comes up with the same upstreams and the same working key, and
// the old locations stop holding anything.
func TestMigrateUpstreamsToExternal_MovesUpstreamsAndKey(t *testing.T) {
	settings := newMemSettings()
	settings.values[SettingUpstreams] = `[
		{"id":"u1","name":"Primary","baseUrl":"https://api.example.com/v1","models":["a","b"],"enabled":false},
		{"id":"u2","name":"Backup","baseUrl":"https://backup.example.com/v1","models":["c"],"enabled":true}
	]`
	secrets := newMemSecrets()
	secrets.values[SecretScope+"/"+SecretAPIKey] = "operator-key"
	external := newMemExternal()

	require.NoError(t, MigrateUpstreamsToExternal(settings, external, secrets, testLogger()))

	records, err := external.FindAllBySource(store.ExternalAppSourceForContract(ContractName))
	require.NoError(t, err)
	require.Len(t, records, 2, "every upstream moves, not just the active one")

	byID := map[string]*store.ExternalApp{}
	for _, r := range records {
		byID[r.ID] = r
	}
	require.Contains(t, byID, "u1")
	require.Contains(t, byID, "u2")

	assert.Equal(t, "https://api.example.com/v1", byID["u1"].URL)
	assert.Equal(t, "Primary", byID["u1"].Name)
	assert.Equal(t, "a\nb", byID["u1"].Value(ContractName, ValueModels))
	assert.Equal(t, "", byID["u1"].Value(ContractName, ValueEnabled), "a disabled upstream stays disabled")
	assert.Equal(t, "true", byID["u2"].Value(ContractName, ValueEnabled))

	// The credential lands on the record of the upstream it was used with, and
	// nowhere else.
	assert.Equal(t, "operator-key", secrets.GetAppSecret(store.ExternalSecretScope("u2"), ContractName))
	assert.Equal(t, "", secrets.GetAppSecret(store.ExternalSecretScope("u1"), ContractName),
		"the key was never used with the disabled upstream, so it does not move there")

	// The old locations are empty, which is what makes the migration its own
	// marker.
	assert.Equal(t, "", settings.values[SettingUpstreams])
	assert.Equal(t, "", secrets.values[SecretScope+"/"+SecretAPIKey])
}

// Running it twice must not double the records or resurrect the old key.
func TestMigrateUpstreamsToExternal_Idempotent(t *testing.T) {
	settings := newMemSettings()
	settings.values[SettingUpstreams] = `[{"id":"u1","name":"Only","baseUrl":"https://a.example.com/v1","enabled":true}]`
	secrets := newMemSecrets()
	secrets.values[SecretScope+"/"+SecretAPIKey] = "k"
	external := newMemExternal()

	require.NoError(t, MigrateUpstreamsToExternal(settings, external, secrets, testLogger()))
	require.NoError(t, MigrateUpstreamsToExternal(settings, external, secrets, testLogger()))

	records, err := external.FindAllBySource(store.ExternalAppSourceForContract(ContractName))
	require.NoError(t, err)
	assert.Len(t, records, 1)
	assert.Equal(t, "k", secrets.GetAppSecret(store.ExternalSecretScope("u1"), ContractName))
}

// A fresh instance has no `ai_upstreams` key at all. The migration must read
// that as "nothing to do" rather than as an empty list it should write down.
func TestMigrateUpstreamsToExternal_NoOpWhenNothingConfigured(t *testing.T) {
	settings := newMemSettings()
	external := newMemExternal()

	require.NoError(t, MigrateUpstreamsToExternal(settings, external, newMemSecrets(), testLogger()))

	records, err := external.FindAllBySource(store.ExternalAppSourceForContract(ContractName))
	require.NoError(t, err)
	assert.Empty(t, records)
}

// An unreadable legacy value is left where it is. Deleting data nobody can read
// is worse than a warning, and refusing to boot over it is worse still.
func TestMigrateUpstreamsToExternal_UnreadableValueLeftInPlace(t *testing.T) {
	settings := newMemSettings()
	settings.values[SettingUpstreams] = "this is not json"
	external := newMemExternal()

	require.NoError(t, MigrateUpstreamsToExternal(settings, external, newMemSecrets(), testLogger()))
	assert.Equal(t, "this is not json", settings.values[SettingUpstreams])
	assert.Empty(t, external.records)
}

// The round trip the resolver depends on: a record written by the migration
// converts back to the upstream the settings API would have served, so a
// migrated instance reports the same list it did before.
func TestUpstreamRoundTripsThroughExternalRecord(t *testing.T) {
	want := Upstream{
		ID:      "u1",
		Name:    "Primary",
		BaseURL: "https://api.example.com/v1",
		Models:  []string{"gpt-4o", "gpt-4o-mini"},
		Enabled: true,
	}

	got, ok := UpstreamFromExternal(ExternalForUpstream(want))
	require.True(t, ok)
	assert.Equal(t, want, got)
}

// A record that is not an AI upstream must not be mistaken for one, or a
// launcher in the registry would show up in the AI Settings list.
func TestUpstreamFromExternalRejectsOtherSources(t *testing.T) {
	for _, src := range []string{
		store.ExternalAppSourceForApp("affine"),
		store.ExternalAppSourceForContract("pvr"),
		"",
		"garbage",
	} {
		_, ok := UpstreamFromExternal(&store.ExternalApp{Source: src})
		assert.False(t, ok, "source %q is not an AI upstream", src)
	}
}
