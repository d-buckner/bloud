package reconciler

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntentTypes_AllImplementIntent(t *testing.T) {
	intents := []Intent{
		NewInstallAppIntent("jellyfin"),
		NewUninstallAppIntent("jellyfin", false),
		NewRenameAppIntent("jellyfin", "Jellyfin Media"),
		NewSetTailnetIntent("home", "tailscale", "tskey-xxx", "https://controlplane.tailscale.com"),
		NewDeleteTailnetIntent(),
		NewAddRemoteAppIntent("app-123", "100.64.0.1:8096", "remote-host"),
		NewDeleteRemoteAppIntent("remote-app-456"),
		NewCreateShareIntent("jellyfin"),
		NewRevokeShareIntent("share-789"),
		NewClearAppDataIntent("jellyfin"),
	}

	for _, intent := range intents {
		assert.NotEmpty(t, intent.IntentID(), "IntentID() should return a non-empty string")
	}

	require.Len(t, intents, 10, "should have exactly 10 intent types")
}

func TestIntentTypes_ConstructorsGenerateUniqueIDs(t *testing.T) {
	a := NewInstallAppIntent("jellyfin")
	b := NewInstallAppIntent("jellyfin")
	assert.NotEqual(t, a.IntentID(), b.IntentID(), "two intents of the same type should have different IDs")

	c := NewUninstallAppIntent("jellyfin", false)
	d := NewUninstallAppIntent("jellyfin", false)
	assert.NotEqual(t, c.IntentID(), d.IntentID())
}

func TestInstallAppIntent_FieldsArePreserved(t *testing.T) {
	intent := NewInstallAppIntent("radarr")
	assert.Equal(t, "radarr", intent.AppName)
	assert.NotEmpty(t, intent.IntentID())
}

func TestUninstallAppIntent_FieldsArePreserved(t *testing.T) {
	intent := NewUninstallAppIntent("sonarr", true)
	assert.Equal(t, "sonarr", intent.AppName)
	assert.True(t, intent.ClearData)

	intentNoClear := NewUninstallAppIntent("sonarr", false)
	assert.False(t, intentNoClear.ClearData)
}

func TestSetTailnetIntent_FieldsArePreserved(t *testing.T) {
	intent := NewSetTailnetIntent("home-net", "tailscale", "tskey-abc", "https://control.example.com")
	assert.Equal(t, "home-net", intent.Name)
	assert.Equal(t, "tailscale", intent.Type)
	assert.Equal(t, "tskey-abc", intent.AuthKey)
	assert.Equal(t, "https://control.example.com", intent.ControlURL)
}

func TestAddRemoteAppIntent_FieldsArePreserved(t *testing.T) {
	intent := NewAddRemoteAppIntent("app-42", "100.64.0.5:9090", "media-server")
	assert.Equal(t, "app-42", intent.AppID)
	assert.Equal(t, "100.64.0.5:9090", intent.TailnetAddr)
	assert.Equal(t, "media-server", intent.HostLabel)
}
