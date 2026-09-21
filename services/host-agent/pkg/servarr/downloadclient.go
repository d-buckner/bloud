// SPDX-License-Identifier: AGPL-3.0-only

package servarr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
)

const (
	// ImplementationQBittorrent is the Servarr implementation id of the
	// qBittorrent download-client type: the value /downloadclient reports for
	// an entry of that kind and /downloadclient/schema names.
	ImplementationQBittorrent = "QBittorrent"

	// downloadClientContract, downloadClientName and downloadClientProtocol
	// are the rest of the entry's identity, read from the
	// /downloadclient/schema of the images Bloud pins. A different
	// implementation would need its own payload, which is why they are
	// constants and not parameters of DownloadClientSpec.
	downloadClientContract = "QBittorrentSettings"
	downloadClientProtocol = "torrent"

	// downloadClientName is the name Bloud's own client carries. It is
	// deliberately not the vendor default ("qBittorrent"): Servarr requires a
	// unique name per entry, so taking the default would collide with a client
	// the operator added under that name and leave Bloud's qBittorrent unwired.
	downloadClientName = "qBittorrent (Bloud)"

	// fieldHost and fieldPort are the two fields that identify the entry as
	// Bloud's (see downloadClientResource).
	fieldHost = "host"
	fieldPort = "port"

	// duplicateNameError is the shared validator's rejection for a document
	// whose name another entry holds ("Should be unique"), answered as 400 by
	// POST /downloadclient. It is a duplicate, not a misconfiguration.
	duplicateNameError = "Should be unique"

	// downloadClientPriority is the vendor default for a torrent client; the
	// field decides the order clients are asked for a release.
	downloadClientPriority = 1

	// APIKeyHeader is the header every Servarr API request authenticates with
	// (the API also accepts ?apikey=). pkg/servarr's own client sends it as
	// apiKeyHeader; consumers that make their own calls through pkg/appclient
	// (the root-folder step Sonarr and Radarr own) send the same header
	// through this name rather than repeating the literal.
	APIKeyHeader = apiKeyHeader
)

// DownloadClientSpec is the qBittorrent download client one Servarr instance
// should hold: the transport the calls need, the provider's address inside
// apps-net, and the instance's own category.
//
// Sonarr and Radarr share the QBittorrentSettings contract field for field
// except for the category, which the contract spells tvCategory in Sonarr and
// movieCategory in Radarr, hence CategoryField.
type DownloadClientSpec struct {
	// APIPath is the instance's versioned API root without slashes: "api/v3"
	// for Sonarr and Radarr.
	APIPath string

	// APIKey is the instance's own API key, sent as X-Api-Key.
	APIKey string

	// Host is the provider's container DNS name inside apps-net, e.g.
	// "apps-qbittorrent" (container names are "apps-<catalogID>").
	Host string

	// Port is the provider's published WebUI port.
	Port int

	// CategoryField is the per-app name of the category field in the
	// QBittorrentSettings contract: "tvCategory" (Sonarr) or "movieCategory"
	// (Radarr).
	CategoryField string

	// Category is the value stored in CategoryField, e.g. "tv-sonarr".
	Category string
}

// path is the download-client collection under this instance's API root. The
// leading slash matters: appclient joins the base URL and the path by
// concatenation.
func (s DownloadClientSpec) path() string {
	return "/" + strings.Trim(s.APIPath, "/") + "/downloadclient"
}

// validate checks the address and category the payload needs.
func (s DownloadClientSpec) validate() error {
	if err := s.validateTransport(); err != nil {
		return err
	}
	if s.Host == "" || s.Port == 0 || s.CategoryField == "" {
		return fmt.Errorf("servarr: download client: incomplete spec (host %q, port %d, category field %q)", s.Host, s.Port, s.CategoryField)
	}
	return nil
}

// validateTransport checks the coordinates every download-client call needs,
// including the ones that never build a payload (pruning).
func (s DownloadClientSpec) validateTransport() error {
	if strings.TrimSpace(s.APIPath) == "" {
		return fmt.Errorf("servarr: download client: empty API path")
	}
	if s.APIKey == "" {
		return fmt.Errorf("servarr: download client: no API key")
	}
	return nil
}

// downloadClientField is one entry of the settings contract's fields list.
// Values are heterogeneous (string, int, bool), so the type is any.
type downloadClientField struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// downloadClientPayload is the body POST /downloadclient expects. It is also
// the body POST /downloadclient/test takes, which is what makes the test call
// a proof that these exact settings work.
type downloadClientPayload struct {
	Name           string                `json:"name"`
	Implementation string                `json:"implementation"`
	ConfigContract string                `json:"configContract"`
	Protocol       string                `json:"protocol"`
	Priority       int                   `json:"priority"`
	Enable         bool                  `json:"enable"`
	Tags           []string              `json:"tags"`
	Fields         []downloadClientField `json:"fields"`
}

// payload builds the client entry. Every field the contract offers is sent
// explicitly (including the empty urlBase/username/password and useSsl=false),
// so the stored entry does not depend on the app's own defaults.
func (s DownloadClientSpec) payload() downloadClientPayload {
	return downloadClientPayload{
		Name:           downloadClientName,
		Implementation: ImplementationQBittorrent,
		ConfigContract: downloadClientContract,
		Protocol:       downloadClientProtocol,
		Priority:       downloadClientPriority,
		Enable:         true,
		Tags:           []string{},
		Fields: []downloadClientField{
			{Name: fieldHost, Value: s.Host},
			{Name: fieldPort, Value: s.Port},
			{Name: "useSsl", Value: false},
			{Name: "urlBase", Value: ""},
			{Name: "username", Value: ""},
			{Name: "password", Value: ""},
			{Name: s.CategoryField, Value: s.Category},
		},
	}
}

// downloadClientResource is the part of a /downloadclient entry this package
// reads: the id (pruning deletes by id), the name, and the fields that say
// whose entry it is.
//
// Identity is the host and port Bloud wrote, not the implementation. Several
// qBittorrent clients can live on one instance (an operator's seedbox, a
// second local daemon), and the Servarr type id is shared by all of them: a
// check on it alone stops wiring Bloud's own client when a foreign one exists,
// and a prune on it alone deletes the operator's clients when the provider
// goes away.
type downloadClientResource struct {
	ID             int                   `json:"id"`
	Name           string                `json:"name"`
	Implementation string                `json:"implementation"`
	Fields         []downloadClientField `json:"fields"`
}

// field returns the named entry's value rendered as a string, or "" when the
// field is absent. Host is a string field and port comes back as a JSON number,
// so the rendering has to handle both.
func (d downloadClientResource) field(name string) string {
	for _, f := range d.Fields {
		if f.Name == name {
			return fmt.Sprintf("%v", f.Value)
		}
	}
	return ""
}

// isBloudEntry reports whether an entry is the one this spec describes: the
// qBittorrent implementation at the host and port Bloud writes. See
// downloadClientResource for why the coordinates, and not the implementation,
// are the identity.
func (s DownloadClientSpec) isBloudEntry(dc downloadClientResource) bool {
	return dc.Implementation == ImplementationQBittorrent &&
		dc.field(fieldHost) == s.Host &&
		dc.field(fieldPort) == strconv.Itoa(s.Port)
}

// EnsureDownloadClient makes the instance hold one qBittorrent download client
// and proves it works. Idempotent: an instance that already has one is left
// untouched (no write, no test call). Returns whether the client was added.
//
// The document is tested before it is stored, and the order is load-bearing:
// /downloadclient/test runs the resource's shared validator, whose
// "Name must be unique" rule compares the submitted document against every
// stored entry, so a body tested *after* its own create is rejected with
// 400 Name/Should be unique (verified against the pinned Sonarr image:
// test-then-create 200, the same body tested after the create 400). Testing
// first validates the identical document against the provider (which is where
// qBittorrent's subnet whitelist plus empty credentials have to authenticate)
// and the create then stores exactly what passed.
func EnsureDownloadClient(ctx context.Context, cl *appclient.Client, spec DownloadClientSpec) (bool, error) {
	if err := spec.validate(); err != nil {
		return false, err
	}
	clients, err := listDownloadClients(ctx, cl, spec)
	if err != nil {
		return false, err
	}
	for _, dc := range clients {
		if spec.isBloudEntry(dc) {
			return false, nil
		}
	}

	body := spec.payload()
	if err := cl.POST(spec.path()+"/test").
		Header(APIKeyHeader, spec.APIKey).
		JSON(body).
		OK(http.StatusOK).
		Exec(ctx); err != nil {
		return false, fmt.Errorf("%s: the qBittorrent download client test failed: %w", cl.Name(), err)
	}
	// The create is never retried. It is not idempotent (the resource's shared
	// validator rejects a document whose name another entry already holds, so a
	// retry of a create that actually landed comes back 400 "Should be unique")
	// and it does not need to be: the test above is the side-effect-free probe,
	// and its 200 already proved these settings work. A name collision is
	// classified as already done, so an operator who keeps their own client
	// under Bloud's name cannot fail the node.
	created, err := cl.POST(spec.path()).
		Header(APIKeyHeader, spec.APIKey).
		JSON(body).
		OK(http.StatusOK, http.StatusCreated).
		NoRetry().
		AlreadyDoneFunc(func(status int, respBody []byte) bool {
			return status == http.StatusBadRequest && bytes.Contains(respBody, []byte(duplicateNameError))
		}).
		Ensure(ctx)
	if err != nil {
		return false, fmt.Errorf("%s: adding the qBittorrent download client: %w", cl.Name(), err)
	}
	return created, nil
}

// RemoveDownloadClient deletes the download client this spec describes (the
// entry at Bloud's host and port) and reports whether anything was removed.
// Servarr has no delete-by-name, so the list is read and the match deleted by
// id. Entries the operator added (a seedbox, a second daemon) are left alone:
// the provider being gone says nothing about them.
//
// Pruning is what an uninstalled provider leaves behind: without it the stored
// hostname stops resolving and every grab fails against a dead target.
func RemoveDownloadClient(ctx context.Context, cl *appclient.Client, spec DownloadClientSpec) (bool, error) {
	if err := spec.validateTransport(); err != nil {
		return false, err
	}
	clients, err := listDownloadClients(ctx, cl, spec)
	if err != nil {
		return false, err
	}

	removed := false
	for _, dc := range clients {
		if !spec.isBloudEntry(dc) {
			continue
		}
		if err := cl.DELETE(fmt.Sprintf("%s/%d", spec.path(), dc.ID)).
			Header(APIKeyHeader, spec.APIKey).
			OK(http.StatusOK).
			Exec(ctx); err != nil {
			return false, fmt.Errorf("%s: removing the stale %s download client: %w", cl.Name(), dc.Name, err)
		}
		removed = true
	}
	return removed, nil
}

// listDownloadClients reads /downloadclient. A non-2xx (401/403 when the key is
// wrong, 5xx while the app boots) surfaces as an appclient HTTPError naming the
// instance and status.
func listDownloadClients(ctx context.Context, cl *appclient.Client, spec DownloadClientSpec) ([]downloadClientResource, error) {
	body, err := cl.GET(spec.path()).
		Header(APIKeyHeader, spec.APIKey).
		OK(http.StatusOK).
		Do(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s: reading download clients: %w", cl.Name(), err)
	}

	var clients []downloadClientResource
	if err := json.Unmarshal(body, &clients); err != nil {
		// The GET accepted exactly one status, so a decode failure is always
		// attributable to that 200 response.
		return nil, fmt.Errorf("%s: reading download clients → %d: decode JSON: %w",
			cl.Name(), http.StatusOK, err)
	}
	// A 200 whose body is `null` decodes to a nil slice and would otherwise be
	// indistinguishable from "this instance has no download clients", which
	// would add a second client next to the one that exists. `[]` decodes to an
	// empty non-nil slice, so the two are exactly separable.
	if clients == nil {
		return nil, fmt.Errorf("%s: reading download clients → %d: empty JSON document", cl.Name(), http.StatusOK)
	}
	return clients, nil
}
