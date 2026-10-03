// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docCatalog is a catalog shaped like the real one: two system apps, user
// apps across every login strategy, one app with no description, and names
// whose case ordering differs from a byte sort.
func docCatalog() map[string]*AppMetadata {
	return map[string]*AppMetadata{
		"authentik": {
			Name:        "authentik",
			DisplayName: "Authentik",
			Description: "Open-source identity provider",
			Category:    "security",
			IsSystem:    true,
		},
		"traefik": {
			Name:        "traefik",
			DisplayName: "Traefik",
			Description: "Cloud-native reverse proxy",
			Category:    "network",
			IsSystem:    true,
		},
		"qbittorrent": {
			Name:        "qbittorrent",
			DisplayName: "qBittorrent",
			Description: "BitTorrent client with a web interface",
			Category:    "media",
			SSO:         SSOConfig{Strategy: "forward-auth"},
		},
		"radicale": {
			Name:        "radicale",
			DisplayName: "Radicale",
			Description: "CalDAV and CardDAV server",
			Category:    "productivity",
			SSO:         SSOConfig{Strategy: "ldap"},
		},
		"affine": {
			Name:        "affine",
			DisplayName: "AFFiNE",
			Description: "Knowledge base for docs and whiteboards",
			Category:    "productivity",
			SSO:         SSOConfig{Strategy: "native-oidc"},
		},
		"jellyfin": {
			Name:        "jellyfin",
			DisplayName: "Jellyfin",
			Description: "Free software media system",
			Category:    "media",
			SSO:         SSOConfig{Strategy: "ldap"},
		},
		"seerr": {
			Name:        "seerr",
			DisplayName: "Seerr",
			Description: "Request manager for your media server",
			Category:    "media",
			SSO:         SSOConfig{Strategy: "none"},
		},
		"mystery": {
			Name:        "mystery",
			DisplayName: "Mystery App",
			Category:    "productivity",
		},
	}
}

// docTarget writes a document holding both generated blocks with stale
// contents, and returns the root to pass to the command helpers.
func docTarget(t *testing.T, listBody, tableBody string) string {
	t.Helper()
	dir := t.TempDir()
	content := "# Bloud\n\nIntro prose.\n\n" +
		catalogListBeginMarker + "\n" + listBody + "\n" + catalogListEndMarker + "\n\n" +
		loginTableBeginMarker + "\n" + tableBody + "\n" + loginTableEndMarker + "\n\n" +
		"Outro prose.\n"
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	return dir
}

func TestRenderCatalogListBulletsEveryUserAppWithItsDescription(t *testing.T) {
	rendered := renderCatalogList(docCatalog())

	for _, want := range []string{
		"- **AFFiNE**: Knowledge base for docs and whiteboards",
		"- **Jellyfin**: Free software media system",
		"- **qBittorrent**: BitTorrent client with a web interface",
		"- **Radicale**: CalDAV and CardDAV server",
		"- **Seerr**: Request manager for your media server",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("bullet %q missing from:\n%s", want, rendered)
		}
	}

	// System apps are not installable, so they never appear as bullets.
	for _, unwanted := range []string{"**Authentik**:", "**Traefik**:"} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("system app bullet %q should not be listed:\n%s", unwanted, rendered)
		}
	}
}

func TestRenderCatalogListAnAppWithNoDescriptionIsNamedAlone(t *testing.T) {
	rendered := renderCatalogList(docCatalog())
	if !strings.Contains(rendered, "- **Mystery App**\n") {
		t.Errorf("an app with no description should be listed by name alone:\n%s", rendered)
	}
	if strings.Contains(rendered, "- **Mystery App**: \n") {
		t.Errorf("an app with no description should not get an empty clause:\n%s", rendered)
	}
}

func TestRenderCatalogListSortsCaseInsensitively(t *testing.T) {
	rendered := renderCatalogList(docCatalog())
	var order []string
	for _, line := range strings.Split(rendered, "\n") {
		rest, ok := strings.CutPrefix(line, "- **")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "**")
		order = append(order, name)
	}
	want := []string{"AFFiNE", "Jellyfin", "Mystery App", "qBittorrent", "Radicale", "Seerr"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", order, want)
	}
}

func TestRenderCatalogListNamesSystemAppsWithTheirCategory(t *testing.T) {
	rendered := renderCatalogList(docCatalog())
	want := "Plus the system apps: **Authentik** (security) and **Traefik** (network)."
	if !strings.Contains(rendered, want) {
		t.Errorf("system apps line missing, want %q in:\n%s", want, rendered)
	}
}

func TestRenderCatalogListWithNoSystemAppsSaysSo(t *testing.T) {
	apps := map[string]*AppMetadata{
		"jellyfin": {Name: "jellyfin", DisplayName: "Jellyfin", SSO: SSOConfig{Strategy: "ldap"}},
	}
	if got := renderCatalogList(apps); !strings.Contains(got, "Bloud ships no system apps.") {
		t.Errorf("want the no-system-apps line, got:\n%s", got)
	}
}

func TestRenderCatalogListCarriesNoCounts(t *testing.T) {
	// The generated block deliberately says nothing about how many apps
	// there are: a count is a second thing to keep in sync, and the list
	// directly under it already answers the question.
	rendered := renderCatalogList(docCatalog())
	for _, unwanted := range []string{"Six apps", "six apps", "5 apps", "two system"} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("generated list should not name counts, found %q in:\n%s", unwanted, rendered)
		}
	}
}

func TestRenderLoginTableGroupsAppsByStrategy(t *testing.T) {
	rendered := renderLoginTable(docCatalog())
	want := []string{
		// Seerr is here rather than under "App-local accounts" because of the
		// QUIRKS override; see TestLoginQuirkMovesSeerrIntoTheLDAPRow.
		"| **LDAP** | Jellyfin, Radicale, Seerr |",
		"| **Forward auth** | qBittorrent |",
		"| **Native OIDC** | AFFiNE |",
		"| **App-local accounts** | Mystery App |",
	}
	for _, row := range want {
		if !strings.Contains(rendered, row) {
			t.Errorf("row %q missing from:\n%s", row, rendered)
		}
	}
}

// TestLoginQuirkMovesSeerrIntoTheLDAPRow pins the one override the README
// needs: Seerr declares `none` at the ingress but its users sign in with
// their Jellyfin account, so drawing it under "App-local accounts" tells a
// Bloud user they need a credential they do not have.
func TestLoginQuirkMovesSeerrIntoTheLDAPRow(t *testing.T) {
	quirk, ok := findLoginQuirk("seerr")
	if !ok {
		t.Fatal("no login quirk declared for seerr")
	}
	if quirk.strategy != "ldap" {
		t.Errorf("seerr quirk strategy = %q, want ldap", quirk.strategy)
	}
	if strings.TrimSpace(quirk.reason) == "" {
		t.Error("the seerr quirk must carry the reason it exists")
	}

	rendered := renderLoginTable(docCatalog())
	if strings.Contains(rendered, "App-local accounts** | Mystery App, Seerr") {
		t.Errorf("seerr should not share the app-local-accounts row:\n%s", rendered)
	}
}

// TestLoginQuirkLeavesOtherAppsOnTheirDeclaredRow guards the override
// against being read as a general reshuffle: only the named app moves.
func TestLoginQuirkLeavesOtherAppsOnTheirDeclaredRow(t *testing.T) {
	for _, app := range []string{"jellyfin", "radicale", "qbittorrent", "affine", "mystery"} {
		if _, ok := findLoginQuirk(app); ok {
			t.Errorf("unexpected quirk for %q", app)
			continue
		}
		meta := docCatalog()[app]
		if got := loginTableRowStrategy(meta); got != normalizeStrategy(meta.SSO.Strategy) {
			t.Errorf("%s row strategy = %q, want its declared %q", app, got, normalizeStrategy(meta.SSO.Strategy))
		}
	}
}

func TestCheckLoginQuirksAcceptsTheRepoCatalog(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	apps, err := loadAppMetadata(filepath.Join(root, "apps"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if problems := checkLoginQuirks(apps); len(problems) > 0 {
		t.Errorf("the repo catalog should satisfy every quirk, got:\n  - %s", strings.Join(problems, "\n  - "))
	}
}

// TestCheckLoginQuirksRejectsStaleOverrides is the ratchet half: an
// override that guards nothing is itself a failure, so the table cannot
// keep asserting something the catalog stopped being true of.
func TestCheckLoginQuirksRejectsStaleOverrides(t *testing.T) {
	original := loginQuirks
	t.Cleanup(func() { loginQuirks = original })

	apps := docCatalog()

	cases := []struct {
		name    string
		quirks  []loginQuirk
		wantSub string
	}{
		{
			name:    "app no longer in the catalog",
			quirks:  []loginQuirk{{app: "removed-app", strategy: "ldap", reason: "gone"}},
			wantSub: "not in the catalog",
		},
		{
			name:    "override agrees with the declared strategy",
			quirks:  []loginQuirk{{app: "radicale", strategy: "ldap", reason: "already ldap"}},
			wantSub: "does nothing",
		},
		{
			name:    "target is a system app",
			quirks:  []loginQuirk{{app: "authentik", strategy: "none", reason: "not listed"}},
			wantSub: "system app",
		},
		{
			name:    "strategy has no README label",
			quirks:  []loginQuirk{{app: "seerr", strategy: "ldap-through-the-magic-sock", reason: "typo"}},
			wantSub: "no README label",
		},
		{
			name:    "reason is blank",
			quirks:  []loginQuirk{{app: "seerr", strategy: "ldap", reason: "  "}},
			wantSub: "no reason",
		},
		{
			name: "two overrides for one app",
			quirks: []loginQuirk{
				{app: "seerr", strategy: "ldap", reason: "one"},
				{app: "seerr", strategy: "none", reason: "two"},
			},
			wantSub: "duplicate quirk",
		},
	}

	for _, tc := range cases {
		loginQuirks = tc.quirks
		problems := checkLoginQuirks(apps)
		if len(problems) == 0 {
			t.Errorf("%s: expected a problem, got none", tc.name)
			continue
		}
		found := false
		for _, p := range problems {
			if strings.Contains(p, tc.wantSub) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: want a problem containing %q, got:\n  - %s", tc.name, tc.wantSub, strings.Join(problems, "\n  - "))
		}
	}
}

func TestRenderLoginTableKeepsTheDocumentedRowOrder(t *testing.T) {
	rendered := renderLoginTable(docCatalog())
	lines := strings.Split(strings.TrimSpace(rendered), "\n")
	var labels []string
	for _, line := range lines {
		if strings.HasPrefix(line, "| **") {
			labels = append(labels, strings.Trim(strings.Split(line, "|")[1], " *"))
		}
	}
	want := []string{"LDAP", "Forward auth", "Native OIDC", "App-local accounts"}
	if strings.Join(labels, ",") != strings.Join(want, ",") {
		t.Errorf("row order = %v, want %v", labels, want)
	}
}

func TestRenderLoginTableUnknownStrategyStillGetsARow(t *testing.T) {
	apps := map[string]*AppMetadata{
		"samlthing": {Name: "samlthing", DisplayName: "Saml Thing", SSO: SSOConfig{Strategy: "saml-web"}},
		"jellyfin":  {Name: "jellyfin", DisplayName: "Jellyfin", SSO: SSOConfig{Strategy: "ldap"}},
	}
	rendered := renderLoginTable(apps)
	// A strategy this command has never seen is labeled from its own name and
	// drawn after the known ones, so a new strategy shows up in the README
	// instead of being silently dropped from it.
	if !strings.Contains(rendered, "| **Saml web** | Saml Thing |") {
		t.Errorf("unknown strategy row missing from:\n%s", rendered)
	}
	if strings.Index(rendered, "Saml web") < strings.Index(rendered, "| **LDAP** |") {
		t.Errorf("unknown strategies should sort after the documented ones:\n%s", rendered)
	}
}

func TestNormalizeStrategy(t *testing.T) {
	for _, in := range []string{"", "  ", "none"} {
		if got := normalizeStrategy(in); got != "none" {
			t.Errorf("normalizeStrategy(%q) = %q, want none", in, got)
		}
	}
	if got := normalizeStrategy(" ldap "); got != "ldap" {
		t.Errorf("normalizeStrategy(\" ldap \") = %q, want ldap", got)
	}
}

func TestHumanize(t *testing.T) {
	cases := map[string]string{
		"native-oidc":  "Native oidc",
		"forward_auth": "Forward auth",
		"ldap":         "Ldap",
		"":             "",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinEnglish(t *testing.T) {
	cases := map[string]string{
		"a":       "a",
		"a|b":     "a and b",
		"a|b|c":   "a, b, and c",
		"a|b|c|d": "a, b, c, and d",
		"":        "",
		"|":       " and ",
		"a|":      "a and ",
	}
	for in, want := range cases {
		var items []string
		if in != "" {
			items = strings.Split(in, "|")
		}
		if got := joinEnglish(items); got != want {
			t.Errorf("joinEnglish(%v) = %q, want %q", items, got, want)
		}
	}
}

func TestRenderCatalogDocIsDeterministic(t *testing.T) {
	// The merge-to-main refresh commits only when the file changed, so the
	// render has to be a pure function of the catalog: no map-iteration
	// order leaking into the output.
	first := catalogDocSections(docCatalog())
	for pass := 0; pass < 5; pass++ {
		again := catalogDocSections(docCatalog())
		for i := range first {
			if first[i].generated != again[i].generated {
				t.Fatalf("pass %d changed section %q:\nfirst:\n%s\nagain:\n%s",
					pass, first[i].block.Label, first[i].generated, again[i].generated)
			}
		}
	}
}

func TestWriteCatalogDocReplacesBothBlocksAndKeepsTheRest(t *testing.T) {
	root := docTarget(t, "OLD LIST", "OLD TABLE")
	path := filepath.Join(root, "README.md")

	if code := writeCatalogDoc(root, "README.md", catalogDocSections(docCatalog())); code != 0 {
		t.Fatalf("write exit = %d, want 0", code)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	got := string(content)

	if strings.Contains(got, "OLD LIST") || strings.Contains(got, "OLD TABLE") {
		t.Errorf("stale block bodies survived the write:\n%s", got)
	}
	// Everything outside the markers is untouched.
	for _, want := range []string{"# Bloud\n\nIntro prose.\n\n", "Outro prose.\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("write changed the document outside the blocks, %q missing:\n%s", want, got)
		}
	}
	// Both blocks are present exactly once.
	for _, marker := range []string{catalogListBeginMarker, catalogListEndMarker, loginTableBeginMarker, loginTableEndMarker} {
		if n := strings.Count(got, marker); n != 1 {
			t.Errorf("marker %q appears %d times, want 1:\n%s", marker, n, got)
		}
	}
}

func TestWriteCatalogDocIsIdempotent(t *testing.T) {
	root := docTarget(t, "OLD LIST", "OLD TABLE")
	path := filepath.Join(root, "README.md")
	sections := catalogDocSections(docCatalog())

	if code := writeCatalogDoc(root, "README.md", sections); code != 0 {
		t.Fatalf("first write exit = %d, want 0", code)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	// The merge-to-main job diffs the file after every write. A writer that
	// added a blank line per pass would make that guard always fire.
	if code := writeCatalogDoc(root, "README.md", sections); code != 0 {
		t.Fatalf("second write exit = %d, want 0", code)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back after second write: %v", err)
	}
	if string(first) != string(again) {
		t.Errorf("second write changed the file:\nfirst:\n%s\nafter:\n%s", first, again)
	}
}

func TestWriteCatalogDocRefusesATargetWithNoMarkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("# Nothing generated here\n"), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if code := writeCatalogDoc(dir, "README.md", catalogDocSections(docCatalog())); code != 1 {
		t.Fatalf("write exit = %d, want 1 on a document with no markers", code)
	}
	content, _ := os.ReadFile(path)
	if strings.Contains(string(content), catalogListBeginMarker) {
		t.Error("write appended a block instead of failing; a marker typo would grow a second copy")
	}
}

func TestCheckCatalogDocReportsWhichBlockIsStale(t *testing.T) {
	sections := catalogDocSections(docCatalog())

	root := docTarget(t, "OLD LIST", "OLD TABLE")
	if code := checkCatalogDoc(root, "README.md", sections); code != 1 {
		t.Fatalf("check exit = %d, want 1 on a stale document", code)
	}

	// A document whose list is current but whose table is not must still fail,
	// and it has to name the table rather than the list.
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	content := "Intro\n\n" +
		strings.TrimRight(sections[0].generated, "\n") + "\n\n" +
		loginTableBeginMarker + "\n| Strategy | Apps |\n|---|---|\n| **LDAP** | Nobody | \n" + loginTableEndMarker + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if code := checkCatalogDoc(dir, "README.md", sections); code != 1 {
		t.Fatalf("check exit = %d, want 1 when only the table is stale", code)
	}
}

func TestCheckCatalogDocPassesOnWhatWriteProduced(t *testing.T) {
	root := docTarget(t, "OLD LIST", "OLD TABLE")
	sections := catalogDocSections(docCatalog())
	if code := writeCatalogDoc(root, "README.md", sections); code != 0 {
		t.Fatalf("write exit = %d, want 0", code)
	}
	if code := checkCatalogDoc(root, "README.md", sections); code != 0 {
		t.Fatalf("check exit = %d, want 0 on a freshly written document", code)
	}
}

func TestCheckCatalogDocFailsWhenABlockIsMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	content := "Intro\n\n" + catalogListBeginMarker + "\nx\n" + catalogListEndMarker + "\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if code := checkCatalogDoc(dir, "README.md", catalogDocSections(docCatalog())); code != 1 {
		t.Fatalf("check exit = %d, want 1 when the login table block is absent", code)
	}
}

// TestRepoREADMECatalogSectionsAreCurrent is the committed-state gate: the
// README in the tree has to be what the catalog in the tree produces. A
// metadata change that leaves the README alone fails here, which is the bug
// this command exists to prevent.
func TestRepoREADMECatalogSectionsAreCurrent(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	apps, err := loadAppMetadata(filepath.Join(root, "apps"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if code := checkCatalogDoc(root, catalogDocDefaultFile, catalogDocSections(apps)); code != 0 {
		t.Fatalf("README catalog sections are stale: run ./bloud catalogdoc --write and commit the result")
	}
}

// TestRepoCatalogListCoversEveryUserApp checks the generated list against the
// catalog on disk rather than against a fixture, so an app added to apps/
// cannot be missing from the README.
func TestRepoCatalogListCoversEveryUserApp(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	appsDir := filepath.Join(root, "apps")
	apps, err := loadAppMetadata(appsDir)
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}

	rendered := renderCatalogList(apps)
	userApps, systemApps := splitCatalog(apps)
	if len(userApps) == 0 || len(systemApps) == 0 {
		t.Fatalf("expected both user and system apps in the catalog, got %d and %d", len(userApps), len(systemApps))
	}

	for _, app := range userApps {
		if !strings.Contains(rendered, "- **"+appDisplayName(app)+"**") {
			t.Errorf("user app %s is missing from the generated catalog list:\n%s", app.Name, rendered)
		}
	}
	for _, app := range systemApps {
		if strings.Contains(rendered, "- **"+appDisplayName(app)+"**") {
			t.Errorf("system app %s should not be a bullet in the user list:\n%s", app.Name, rendered)
		}
		if !strings.Contains(rendered, "**"+appDisplayName(app)+"** (") {
			t.Errorf("system app %s is missing from the system apps line:\n%s", app.Name, rendered)
		}
	}
}

// TestRepoLoginTableCoversEveryUserApp asserts every user app appears in the
// generated table exactly once, under the strategy its metadata declares as
// adjusted by the QUIRKS override.
func TestRepoLoginTableCoversEveryUserApp(t *testing.T) {
	root, err := getProjectRoot()
	if err != nil {
		t.Fatalf("project root: %v", err)
	}
	apps, err := loadAppMetadata(filepath.Join(root, "apps"))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	if problems := checkLoginQuirks(apps); len(problems) > 0 {
		t.Fatalf("quirks out of step with the catalog:\n  - %s", strings.Join(problems, "\n  - "))
	}

	rendered := renderLoginTable(apps)
	userApps, _ := splitCatalog(apps)

	for _, app := range userApps {
		name := appDisplayName(app)
		if countLoginTableEntries(rendered, name) != 1 {
			t.Errorf("%s should appear exactly once in the login table:\n%s", name, rendered)
		}
		strategy := loginTableRowStrategy(app)
		label := loginStrategyLabel(strategy)
		if !loginTableEntryUnderLabel(rendered, name, label) {
			t.Errorf("%s is not under its declared strategy %q (%s):\n%s", name, strategy, label, rendered)
		}
	}
}

// loginTableEntries parses the app names out of one rendered login-table row.
// The cells are comma-separated display names, so the entries are what a
// reader sees as one app, not what a substring search would find.
//
// That distinction is load-bearing here: "Hermes Web UI" contains "Hermes",
// so a raw strings.Count over the rendered table charges the shorter name
// for the longer app's appearance and reports a duplicate that is not one.
func loginTableEntries(row string) []string {
	cells := strings.Split(row, "|")
	if len(cells) < 3 {
		return nil
	}
	cell := strings.TrimSpace(cells[2])
	if cell == "" {
		return nil
	}
	parts := strings.Split(cell, ",")
	entries := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			entries = append(entries, trimmed)
		}
	}
	return entries
}

// countLoginTableEntries counts rows whose cell contains `name` as a whole
// entry, which is the count the test means: how many times this app is
// listed, not how many times its name occurs as characters.
func countLoginTableEntries(rendered, name string) int {
	count := 0
	for _, line := range strings.Split(rendered, "\n") {
		for _, entry := range loginTableEntries(line) {
			if entry == name {
				count++
			}
		}
	}
	return count
}

// loginTableEntryUnderLabel reports whether `name` is listed in a row whose
// strategy label is `label`.
func loginTableEntryUnderLabel(rendered, name, label string) bool {
	for _, line := range strings.Split(rendered, "\n") {
		if !strings.Contains(line, "**"+label+"**") {
			continue
		}
		for _, entry := range loginTableEntries(line) {
			if entry == name {
				return true
			}
		}
	}
	return false
}
