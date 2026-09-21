// SPDX-License-Identifier: AGPL-3.0-only

package prowlarr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/appclient"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// Everything Prowlarr-specific about Bloud's conversation with the instance's
// application-sync resource lives in this file: paths, payload shape, field
// names. The behaviour below was verified against a running instance of the
// pinned image (lscr.io/linuxserver/prowlarr:2.6.5.5623-ls161) and read back
// from Prowlarr v2.6.5.5623 so a future tag bump can re-check each claim:
//
//	Prowlarr.Api.V1/Applications/ApplicationController.cs  route + status codes
//	Prowlarr.Api.V1/ProviderControllerBase.cs              create/delete bodies
//	Prowlarr.Http/REST/RestController.cs                   Created(id) → 201
//	Prowlarr.Http/ClientSchema/SchemaBuilder.cs            apiKey masking
//	NzbDrone.Core/Applications/Sonarr/SonarrSettings.cs    field names + defaults
const (
	// applicationsPath is the application-sync resource: the PVRs Prowlarr
	// pushes its indexers into. The leading slash matters, because appclient
	// joins the base URL and the path by concatenation.
	applicationsPath = "/" + apiPath + "/applications"

	// applicationTestPath validates an application document (including its
	// connection to the PVR) without saving it. It is the call that proves
	// the sibling is reachable from inside the Prowlarr container and accepts
	// the key Bloud handed over. It runs the resource's shared validator, so it
	// also rejects a name another entry already holds with 400 "Should be
	// unique": a document can only be tested before it is created, which is the
	// order the reconcile uses (verified against the pinned image: testing
	// after the create always fails).
	applicationTestPath = applicationsPath + "/test"

	// applicationTestAllPath tests every entry the instance already holds
	// (the stored documents, not the one being submitted) and answers 200 with
	// one {id, isValid, validationFailures} record per entry
	// (ProviderControllerBase.TestAll). It is what makes a stored entry
	// verifiable at all: Prowlarr masks the apiKey on read, so a document
	// comparison cannot tell whether the key a PVR now rejects is still the one
	// Prowlarr holds.
	applicationTestAllPath = applicationsPath + "/testall"

	// apiKeyHeader is the header every Prowlarr API request authenticates with
	// (the API also accepts ?apikey=; the header keeps the key out of URLs and
	// logs).
	apiKeyHeader = "X-Api-Key"

	// maskedSecret is what Prowlarr returns in place of every non-empty field
	// it marks PrivacyLevel.ApiKey (SchemaBuilder.ToSchema). SonarrSettings and
	// RadarrSettings both mark their ApiKey that way, so an application's
	// stored key can be written but never read back; see applicationMatches
	// for what that means for drift detection.
	maskedSecret = "********"

	// duplicateNameError is the resource's shared validator failure for a
	// document whose name another entry already holds ("Should be unique").
	// Prowlarr applies it to both POST /applications and
	// POST /applications/test and answers it as 400: a duplicate, not a
	// misconfiguration.
	duplicateNameError = "Should be unique"
)

// applicationField is one name/value pair of an application's fields array.
// Prowlarr renders a much richer document per field (label, type, privacy,
// selectOptions…); only the name and the value are read and written here.
type applicationField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// UnmarshalJSON reads a field document leniently. Prowlarr fills an application
// with every field of its schema, and most of them do not hold strings:
// syncCategories is an array of numbers, syncAnimeStandardFormatSearch is a
// boolean, and an empty advanced field has no value key at all. Decoding the
// list strictly would fail on the first entry Bloud does not own, so only a
// string value is captured and anything else is left empty, which also means it
// can never compare equal to a value Bloud wants.
func (f *applicationField) UnmarshalJSON(data []byte) error {
	var doc struct {
		Name  string          `json:"name"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return err
	}
	f.Name = doc.Name
	f.Value = ""
	var value string
	if err := json.Unmarshal(doc.Value, &value); err == nil {
		f.Value = value
	}
	return nil
}

// application is Prowlarr's application-sync document: one PVR that Prowlarr
// pushes its indexer list into. The json names are the API's camelCase
// spellings. SyncCategories/AnimeSyncCategories are absent on purpose; Bloud
// leaves them at the schema defaults.
type application struct {
	ID             int                `json:"id,omitempty"`
	Name           string             `json:"name"`
	Implementation string             `json:"implementation"`
	ConfigContract string             `json:"configContract"`
	SyncLevel      string             `json:"syncLevel"`
	Tags           []int              `json:"tags"`
	Fields         []applicationField `json:"fields"`
}

// field returns the value of the named entry in the application's fields, or
// "" when the field is absent or carries no value.
func (a application) field(name string) string {
	for _, f := range a.Fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// applicationsAPI is the typed surface over one instance's application-sync
// resource. It holds the instance API key the configurator read from
// config.xml, because every call carries it.
type applicationsAPI struct {
	cl     *appclient.Client
	apiKey string
}

// newApplicationsAPI builds the client against the instance's own base URL.
func newApplicationsAPI(f configurator.ClientFactory, baseURLFn func() string, apiKey string) *applicationsAPI {
	return &applicationsAPI{
		cl:     f.New(appclient.Spec{Name: appName, BaseURLFn: baseURLFn}),
		apiKey: apiKey,
	}
}

// listApplications returns every application the instance has configured.
func (a *applicationsAPI) listApplications(ctx context.Context) ([]application, error) {
	var out []application
	if err := a.cl.GET(applicationsPath).
		Header(apiKeyHeader, a.apiKey).
		OK(http.StatusOK).
		DoInto(ctx, &out); err != nil {
		return nil, fmt.Errorf("prowlarr: listing applications: %w", err)
	}
	return out, nil
}

// createApplication adds an application and reports whether the instance
// created it.
//
// Prowlarr answers 201 Created with the stored document (whose apiKey comes
// back masked). A name that already exists is rejected with 400 and the shared
// validator's "Should be unique" failure: that is a duplicate, not a
// misconfiguration, so it is classified as already done: the caller's next
// reconciliation compares the existing entry field by field and replaces it
// when it drifted.
//
// The call also tests the connection: ApplicationDefinition.Enable is derived
// from SyncLevel, so a fullSync document is tested by the create itself and a
// PVR that rejects the key surfaces as a 400 here.
func (a *applicationsAPI) createApplication(ctx context.Context, app application) (bool, error) {
	created, err := a.cl.POST(applicationsPath).
		Header(apiKeyHeader, a.apiKey).
		JSON(app).
		OK(http.StatusCreated).
		AlreadyDoneFunc(func(status int, body []byte) bool {
			return status == http.StatusBadRequest && bytes.Contains(body, []byte(duplicateNameError))
		}).
		Ensure(ctx)
	if err != nil {
		return false, fmt.Errorf("prowlarr: creating the %s application: %w", app.Implementation, err)
	}
	return created, nil
}

// deleteApplication removes the application with the given id and reports
// whether the instance accepted the removal. Prowlarr answers 200 with an empty
// body, whether or not the id existed (ProviderControllerBase.DeleteProvider),
// so a repeat delete is not distinguishable from the first: the caller only
// deletes when it has just read the entry.
func (a *applicationsAPI) deleteApplication(ctx context.Context, id int) (bool, error) {
	deleted, err := a.cl.DELETE(fmt.Sprintf("%s/%d", applicationsPath, id)).
		Header(apiKeyHeader, a.apiKey).
		OK(http.StatusOK).
		Ensure(ctx)
	if err != nil {
		return false, fmt.Errorf("prowlarr: deleting application %d: %w", id, err)
	}
	return deleted, nil
}

// testApplication runs Prowlarr's own connection test for a document without
// saving it: it validates the document against SonarrSettings/RadarrSettings
// and calls the PVR through the base URL with the apiKey. Prowlarr answers 200
// with an empty body on success and 400 with a validation-error array on
// failure, which the returned error renders as the request, status and body.
//
// The document's name must not already be held by another entry: the endpoint
// runs the resource's shared validator, and a colliding name is rejected with
// "Should be unique" before the connection is even attempted. Callers therefore
// test the document before creating it, and remove a stale entry first.
func (a *applicationsAPI) testApplication(ctx context.Context, app application) error {
	if err := a.cl.POST(applicationTestPath).
		Header(apiKeyHeader, a.apiKey).
		JSON(app).
		OK(http.StatusOK).
		Exec(ctx); err != nil {
		return fmt.Errorf("prowlarr: testing the %s application: %w", app.Implementation, err)
	}
	return nil
}

// updateApplication replaces an existing entry in place and answers 200 with
// the stored document (ProviderControllerBase → RestPutById, which runs the
// same validator and connection test as a create). Repairing through a PUT
// instead of a delete+create keeps whatever the operator owns on the entry:
// its name and its tags.
func (a *applicationsAPI) updateApplication(ctx context.Context, app application) error {
	if err := a.cl.PUT(fmt.Sprintf("%s/%d", applicationsPath, app.ID)).
		Header(apiKeyHeader, a.apiKey).
		JSON(app).
		OK(http.StatusOK).
		Exec(ctx); err != nil {
		return fmt.Errorf("prowlarr: updating application %d (%s): %w", app.ID, app.Implementation, err)
	}
	return nil
}

// applicationTestResult is one record of POST /applications/testall: the
// instance's own verdict on an entry it holds.
type applicationTestResult struct {
	ID      int  `json:"id"`
	IsValid bool `json:"isValid"`
}

// duplicateName reports whether err is the shared validator's "Should be
// unique" rejection: another entry already holds the document's name. Prowlarr
// answers it as 400 from both POST /applications and POST /applications/test.
func duplicateName(err error) bool {
	return appclient.StatusOf(err) == http.StatusBadRequest &&
		bytes.Contains(appclient.BodyOf(err), []byte(duplicateNameError))
}

// testStoredApplications runs the instance's connection test against every
// entry it already holds and returns the verdicts by entry id. An entry that
// answers false is one Prowlarr cannot reach with the settings it stored,
// including the case where the PVR's data was purged and the key it holds is
// no longer the PVR's key.
//
// Not retried: the call performs a live connection test per entry, so a retry
// multiplies a wait that the caller already tolerates.
func (a *applicationsAPI) testStoredApplications(ctx context.Context) (map[int]bool, error) {
	var results []applicationTestResult
	if err := a.cl.POST(applicationTestAllPath).
		Header(apiKeyHeader, a.apiKey).
		OK(http.StatusOK).
		NoRetry().
		DoInto(ctx, &results); err != nil {
		return nil, fmt.Errorf("prowlarr: testing its stored applications: %w", err)
	}
	valid := make(map[int]bool, len(results))
	for _, result := range results {
		valid[result.ID] = result.IsValid
	}
	return valid, nil
}
