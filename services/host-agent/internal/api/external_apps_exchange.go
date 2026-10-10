// SPDX-License-Identifier: AGPL-3.0-only

package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"codeberg.org/d-buckner/bloud/services/host-agent/internal/catalog"
	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// The remote-app form is generated from the contract registry, which is the
// right schema exactly as long as the credential a contract declares is
// something the operator can read out of the remote app's own UI. For one app in
// the catalog it is not: Seerr's admin is a Jellyfin sign-in, and its API key is
// a string most people have never looked at.
//
// These helpers close that gap without special-casing an app in the form. An app
// registers a credential exchange (see pkg/configurator/exchange.go) and three
// things follow: the generated form stops asking for that contract's secrets and
// asks for a sign-in instead; Bloud offers the login it already holds when it
// holds one; and the credential stored on the record is whatever the remote app
// handed back. Everything downstream of the record is unchanged, which is the
// whole point: a consumer cannot tell an exchanged key from a pasted one, and no
// consumer needed to be touched to make this work.
//
// Design note: the exchange runs here, at the API boundary, when the operator
// saves. Not in a reconciliation pass. An external record has no graph node, and
// the resolver has to stay a pure read of stored state (invariant 15), so there
// is no pass to run it in and no probe to run it on. The consequence is honest and
// stated in the form: the key is as current as the moment it was saved, and a
// remote that rotates its key later needs the record re-saved.

// exchangeBudget bounds one sign-in exchange end to end. An exchange is a
// handful of calls to a host the operator named, and any of them can black-hole;
// the default per-request timeout is 15s, so without a whole-operation bound a
// form save could sit on a dead host for the best part of a minute per attempt.
const exchangeBudget = 45 * time.Second

// externalProviderExchange is the sign-in block of a provider option: what to
// ask for instead of the contract's own secret fields, and the login Bloud can
// use when the operator has nothing to type.
type externalProviderExchange struct {
	// Contract names the contract whose secrets this replaces. Those fields are
	// absent from the form, so this says which section the inputs stand for.
	Contract string `json:"contract"`
	// Inputs are the fields to render. Always required, unless StoredLogin is
	// set, in which case they are the alternative to the checkbox.
	Inputs []externalProviderField `json:"inputs"`
	// StoredLogin describes a login Bloud already holds, in words the form can
	// show next to a checkbox. Empty means the operator must type the inputs:
	// there is no local media server, or it never published a login.
	StoredLogin string `json:"storedLogin,omitempty"`
}

// providerExchange builds the exchange block for one catalog app, or nil when
// the app registered no exchange (every app except Seerr today).
func (m *externalAppsModule) providerExchange(app *catalog.App) *externalProviderExchange {
	exchange, ok := configurator.LookupCredentialExchange(app.CatalogID)
	if !ok {
		return nil
	}
	// An exchange must name a contract this app actually offers. A stale
	// registration (the app stopped providing the contract, or the id was wrong)
	// would otherwise produce a form that asks for a sign-in and suppresses
	// nothing, or, worse, hides the fields the contract still needs.
	if _, offered := app.Provides[exchange.Contract()]; !offered {
		m.logger.Warn("registered credential exchange names a contract the app does not provide",
			"app", app.CatalogID, "contract", exchange.Contract())
		return nil
	}
	_, label, hasStored := m.resolveLogin(exchange.LoginContract())
	inputs := make([]externalProviderField, 0, len(exchange.Inputs()))
	for _, input := range exchange.Inputs() {
		kind := "value"
		if input.Secret {
			kind = "secret"
		}
		inputs = append(inputs, externalProviderField{
			Key:      input.Key,
			Label:    input.Label,
			Kind:     kind,
			Required: !hasStored,
			Help:     input.Help,
		})
	}
	return &externalProviderExchange{
		Contract:    exchange.Contract(),
		Inputs:      inputs,
		StoredLogin: label,
	}
}

// exchangedContract reports the contract whose declared secrets an app's
// exchange replaces, so the form can leave them out. An app with no exchange, or
// one whose exchange names a contract the app does not offer, gets nothing
// suppressed: the secrets stay in the form, because with no way to trade for them
// they are the only way to fill the contract.
func exchangedContract(app *catalog.App, contractName string) bool {
	exchange, ok := configurator.LookupCredentialExchange(app.CatalogID)
	if !ok || exchange.Contract() != contractName {
		return false
	}
	_, offered := app.Provides[contractName]
	return offered
}

// applyProviderExchange runs an app's sign-in exchange when the request asked
// for one, and folds what it produced into the request as though the operator had
// typed it. Everything downstream of here is the ordinary path: the credential is
// validated like a posted secret, stored like a posted secret, and read back by
// consumers like a posted secret.
//
// An update that names neither inputs nor the stored login is deliberately left
// alone. That is the existing rule for secrets, and it is what lets a rename
// PATCH go through without a credential it cannot read back.
func (m *externalAppsModule) applyProviderExchange(
	provider *catalog.App,
	endpoint string,
	isAdd bool,
	req *setExternalAppRequest,
) error {
	_, registered := configurator.LookupCredentialExchange(provider.CatalogID)
	asked := req.ExchangeLogin ||
		strings.TrimSpace(req.Exchange[configurator.ExchangeInputUsername]) != "" ||
		strings.TrimSpace(req.Exchange[configurator.ExchangeInputPassword]) != ""
	if !asked {
		if !registered {
			return nil
		}
		// An app with an exchange offers a sign-in instead of a key field, so a
		// save that sent neither would otherwise fail with "contract requires a
		// credential": a true statement that points the operator at a field the
		// form never showed them.
		if isAdd && !containsSecretFor(req.Secrets, provider) {
			return fmt.Errorf(
				"a remote %s needs a sign-in: enter an admin account, or use the login Bloud already has",
				provider.DisplayName)
		}
		return nil
	}
	if !registered {
		return fmt.Errorf("%q has no sign-in exchange; supply its credential directly", provider.DisplayName)
	}

	secrets, values, err := m.runProviderExchange(provider, endpoint, *req)
	if err != nil {
		return err
	}
	if req.Secrets == nil {
		req.Secrets = map[string]string{}
	}
	for contract, value := range secrets {
		req.Secrets[contract] = value
	}
	if req.Values == nil {
		req.Values = map[string]map[string]string{}
	}
	for contract, inner := range values {
		if req.Values[contract] == nil {
			req.Values[contract] = map[string]string{}
		}
		// The exchange's answer wins over anything posted. It is a reading of the
		// running instance, and a static catalog default is a claim about the
		// install Bloud booted, not about this one.
		for key, value := range inner {
			req.Values[contract][key] = value
		}
	}
	return nil
}

// containsSecretFor reports whether the request supplied a credential for any
// contract the provider offers.
func containsSecretFor(secrets map[string]string, provider *catalog.App) bool {
	for _, contract := range sortedContractNames(provider.Provides) {
		if strings.TrimSpace(secrets[contract]) != "" {
			return true
		}
	}
	return false
}

// runProviderExchange performs an app's sign-in exchange against the endpoint the
// operator registered and returns the payload it produced, in the shape the
// record stores: secrets keyed by contract, values keyed by contract.
//
// It is called from decodeProvider, before validation, so what the exchange
// produces is held to exactly the same rules as what a form posts: a required
// value the exchange could not answer fails the save rather than resolving later
// into a consumer binding with a hole in it.
func (m *externalAppsModule) runProviderExchange(
	app *catalog.App,
	endpoint string,
	req setExternalAppRequest,
) (map[string]string, map[string]map[string]string, error) {
	exchange, ok := configurator.LookupCredentialExchange(app.CatalogID)
	if !ok {
		return nil, nil, fmt.Errorf("%q has no sign-in exchange; supply its credential directly", app.DisplayName)
	}
	inputs := map[string]string{}
	for key, value := range req.Exchange {
		inputs[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	// The password is deliberately not trimmed: a credential is bytes, not a
	// sentence, and a trailing space is as much the operator's as any other.
	inputs[configurator.ExchangeInputPassword] = req.Exchange[configurator.ExchangeInputPassword]

	var login *configurator.Login
	if req.ExchangeLogin {
		resolved, _, ok := m.resolveLogin(exchange.LoginContract())
		if !ok {
			return nil, nil, fmt.Errorf(
				"nothing here provides the %s role, so Bloud holds no login to sign in with; type the account instead",
				exchange.LoginContract())
		}
		login = &resolved
	}

	ctx, cancel := context.WithTimeout(context.Background(), exchangeBudget)
	defer cancel()

	result, err := exchange.Exchange(ctx, configurator.ExchangeRequest{
		Endpoint: endpoint,
		Inputs:   inputs,
		Login:    login,
		HTTP:     configurator.ClientFactory{Logger: m.logger},
		Logger:   m.logger,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(result.Secrets) == 0 {
		// An exchange that traded for nothing is not a successful exchange.
		// Accepting it would store a provider record that claims a contract it
		// cannot authenticate against, which is the failure this whole path
		// exists to prevent.
		return nil, nil, fmt.Errorf("%s returned no credential", app.DisplayName)
	}
	values := map[string]map[string]string{}
	for contract, inner := range result.Values {
		if len(inner) == 0 {
			continue
		}
		values[contract] = inner
	}
	if len(values) == 0 {
		values = nil
	}
	return result.Secrets, values, nil
}

// loginContractKeys reports the secret and value keys of a contract that *is* a
// username and password, or ok=false for anything else.
//
// "Login-shaped" is a property of the contract registry, not a per-app rule: a
// contract with exactly one declared secret and exactly one required declared
// value *is* a credential pair, which is how mediaServer documents itself ("the
// username is a value and the password is a secret, because that is what they
// are"). Anything else is not a login, so an app cannot borrow the stored-login
// shortcut for a credential that is not one.
func loginContractKeys(contractName string) (secretKey, valueKey string, ok bool) {
	if contractName == "" {
		return "", "", false
	}
	spec, known := catalog.ContractFor(contractName)
	if !known || len(spec.Secrets) != 1 || len(spec.Values) != 1 || spec.Values[0].Optional {
		return "", "", false
	}
	return spec.Secrets[0], spec.Values[0].Key, true
}

// installedProvidersOffering lists the installed catalog apps that provide a
// contract, sorted by catalog id.
//
// More than one provider of one role is possible in the catalog and there is no
// ranking between them, so the order is made stable rather than convenient: map
// order would make the offered login a different account on each render.
func (m *externalAppsModule) installedProvidersOffering(contractName string) []*catalog.App {
	if m.catalog == nil {
		return nil
	}
	apps, err := m.catalog.GetAll()
	if err != nil {
		return nil
	}
	installed := m.installedCatalogIDs()
	var out []*catalog.App
	for _, app := range apps {
		if app == nil || !installed[app.CatalogID] {
			continue
		}
		if _, offers := app.Provides[contractName]; offers {
			out = append(out, app)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CatalogID < out[j].CatalogID })
	return out
}

// resolveLogin finds the login Bloud holds for a login-shaped contract: the
// installed app that provides it, the credential and username it publishes, and
// a label the form can show beside its checkbox.
//
// This is Bloud reading credentials Bloud minted and stores, on behalf of its own
// machinery. Invariant 15 gates what a *consumer's binding* may contain, and an
// exchange is not a consumer: it is handed the login as an argument and never
// reaches into the secrets manager itself.
func (m *externalAppsModule) resolveLogin(contractName string) (configurator.Login, string, bool) {
	if m.secrets == nil {
		return configurator.Login{}, "", false
	}
	secretKey, valueKey, ok := loginContractKeys(contractName)
	if !ok {
		return configurator.Login{}, "", false
	}
	for _, provider := range m.installedProvidersOffering(contractName) {
		login, label, ok := publishedLogin(provider, contractName, secretKey, valueKey, m.secrets)
		if ok {
			return login, label, true
		}
	}
	return configurator.Login{}, "", false
}

// publishedLogin reads one provider's published login, or reports that it has
// not published a complete one yet.
func publishedLogin(
	provider *catalog.App,
	contractName, secretKey, valueKey string,
	secrets configurator.AppSecretsProvider,
) (configurator.Login, string, bool) {
	password := secrets.GetAppSecret(provider.CatalogID, secretKey)
	if password == "" {
		return configurator.Login{}, "", false
	}
	// A provider may publish the username at runtime (an operator typed it into a
	// remote-install form); the static declaration is the fallback, which is what a
	// Bloud-booted provider's account name is.
	username := secrets.GetAppContractValue(provider.CatalogID, contractName, valueKey)
	if username == "" {
		username = provider.Provides[contractName].Values[valueKey]
	}
	if username == "" {
		return configurator.Login{}, "", false
	}
	label := fmt.Sprintf("the %s login Bloud already has (%s)", provider.DisplayName, username)
	return configurator.Login{Username: username, Password: password}, label, true
}
