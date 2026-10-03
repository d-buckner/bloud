// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"fmt"
	"regexp"
	"strings"
)

// containerIDs maps "app + container" to the mermaid node id assigned to it.
type containerIDs struct {
	byContainer map[string]string
}

func containerKey(appName, containerName string) string {
	return appName + "\x00" + containerName
}

// assignContainerIDs hands every container in the catalog a node id. The id
// is built from the app and the component the container names, which keeps
// the generated source readable, and the assignment is recorded, so an id
// that would collide with another app's node takes a numeric suffix instead
// of silently merging two containers into one box.
func assignContainerIDs(apps map[string]*AppMetadata) *containerIDs {
	ids := &containerIDs{byContainer: make(map[string]string)}
	owner := make(map[string]string)

	for _, appName := range sortedAppNames(apps) {
		app := apps[appName]
		if len(app.Containers) == 0 {
			ids.assign(owner, appName, appName, "c_"+idPart(appName))
			continue
		}
		for _, container := range app.Containers {
			ids.assign(owner, appName, container.Name, containerNodeID(appName, container.Name))
		}
	}
	return ids
}

// assign records one node id, suffixing it if the base id is taken.
func (c *containerIDs) assign(owner map[string]string, appName, containerName, base string) {
	key := containerKey(appName, containerName)
	id := base
	for n := 2; owner[id] != "" && owner[id] != key; n++ {
		id = fmt.Sprintf("%s_%d", base, n)
	}
	owner[id] = key
	c.byContainer[key] = id
}

// get returns the node id assigned to one container.
func (c *containerIDs) get(appName, containerName string) string {
	return c.byContainer[containerKey(appName, containerName)]
}

// nonIDChar matches anything mermaid cannot use inside an identifier.
var nonIDChar = regexp.MustCompile(`[^A-Za-z0-9_]`)

// idPart turns an app or container name into a mermaid-safe identifier part.
func idPart(s string) string {
	return nonIDChar.ReplaceAllString(s, "_")
}

// appBoxID is the subgraph id for an app.
func appBoxID(appName string) string {
	return "app_" + idPart(appName)
}

// containerNodeID is the node id for one container of an app. The app's own
// single container keeps just the app name; a component gets the app name
// and its own.
func containerNodeID(appName, containerName string) string {
	label := containerLabel(containerName, appName)
	if label == appName {
		return "c_" + idPart(appName)
	}
	return "c_" + idPart(appName) + "_" + idPart(label)
}

// containerLabel shortens a runtime container name ("apps-immich-postgres")
// to the component it names ("postgres"), the same way the developer graph
// labels it. The app's own single container keeps the app's name.
func containerLabel(containerName, appID string) string {
	short := strings.TrimPrefix(strings.TrimPrefix(containerName, "apps-"+appID), "-")
	short = strings.TrimPrefix(short, "apps-")
	if short == "" {
		return appID
	}
	return short
}
