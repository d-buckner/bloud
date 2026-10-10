// SPDX-License-Identifier: AGPL-3.0-only

package configurator

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
)

// A CredentialExchange is an app's statement that the credential its contract
// needs is not something the operator of a remote install can paste, but
// something Bloud can obtain by signing in to that install with what they do
// have.
//
// The contract registry says what a provider must *publish*. It has nothing to
// say about how a human supplies that fact for an install Bloud does not boot,
// which is why the remote-app form is generated from it: one field per declared
// secret works whenever the credential is visible in the app's own settings
// page, which is true of a Servarr API key and false of an app whose only
// credential is a login. This interface is the gap: the operator hands over a
// sign-in, the app's own API hands over the key, and everything downstream of
// the record (the stored secret, the resolver, the consumer's binding) is
// exactly the shape it would have been if the key had been typed.
//
// It is registered per catalog app, not per contract, because the trade is a
// property of the app's API: one sign-in is one login, whatever roles the app
// ends up filling.
type CredentialExchange interface {
	// Contract names the contract whose declared secrets this exchange
	// replaces. The form stops asking for those secrets when it is set, so a
	// provider can never be handed both a pasted key and the login that would
	// overwrite it.
	Contract() string

	// Inputs are the fields the operator fills when Bloud holds no login of its
	// own. Every input is required: an exchange with nothing to trade has
	// nothing to do.
	Inputs() []ExchangeInput

	// LoginContract names a contract whose installed provider publishes a login
	// Bloud may use instead of typed inputs, or "" when there is no such
	// shortcut. It is a contract rather than an app name so the resolution stays
	// generic: whoever fills that role here has a login for it.
	LoginContract() string

	// Exchange performs the trade against the remote install and returns the
	// contract payload it produced. It must not write anything to the remote:
	// the exchange reads a credential, it does not rotate or provision one.
	Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error)
}

// ExchangeInput is one field of an exchange, in the shape the generated form
// already renders: a key, a label, and whether it is a secret.
type ExchangeInput struct {
	// Key names the input in ExchangeRequest.Inputs. Lowercase, one word, the
	// same convention as a contract value key.
	Key string
	// Label is what the form shows. Written for a person who is being asked for
	// a credential, not for a protocol.
	Label string
	// Secret marks an input that renders as a password box and is never echoed
	// back, the same rule a contract secret follows.
	Secret bool
	// Help says where the value comes from, because the answer is not always
	// obvious: which account can sign in is the app's business, not the
	// operator's intuition.
	Help string
}

// Login is a username and password pair: the shape every exchange trades, and
// the shape a login-shaped contract publishes.
type Login struct {
	Username string
	Password string
}

// The two input keys an exchange is allowed to read. A sign-in is a username and
// a password, so the framework can pick them out of the operator's inputs
// without every app restating the names, and ExchangeInput exists to render
// them, not to invent a new vocabulary per app.
const (
	ExchangeInputUsername = "username"
	ExchangeInputPassword = "password"
)

// ExchangeRequest carries everything host-shaped, so an exchange is a stateless
// protocol adapter: it needs no Deps, holds no client between calls, and cannot
// reach a store. Everything it may touch arrives here.
type ExchangeRequest struct {
	// Endpoint is the remote origin the operator registered, scheme and host,
	// no path.
	Endpoint string
	// Inputs holds what the operator typed, keyed by ExchangeInput.Key.
	Inputs map[string]string
	// Login is the credential Bloud resolved from the exchange's LoginContract,
	// nil when it holds none. It is the fallback for the boxes the operator left
	// empty, which is the common case: nobody retypes a password they never
	// chose when there is a box that says Bloud already has one.
	Login *Login
	// HTTP builds the client for the remote origin. The zero value is usable.
	HTTP ClientFactory
	// Logger is the host logger. nil means slog.Default().
	Logger *slog.Logger
}

// Chosen returns the sign-in the exchange should use: what the operator typed,
// because typing it is the statement of intent, and Bloud's own login when they
// typed nothing complete. It reports false when neither is a pair: half a
// sign-in is a form that was not filled in, not a credential to guess at.
func (r ExchangeRequest) Chosen() (Login, bool) {
	typed := Login{
		Username: strings.TrimSpace(r.Inputs[ExchangeInputUsername]),
		Password: r.Inputs[ExchangeInputPassword],
	}
	if typed.Username != "" && typed.Password != "" {
		return typed, true
	}
	if r.Login != nil && r.Login.Username != "" && r.Login.Password != "" {
		return *r.Login, true
	}
	return Login{}, false
}

// ExchangeResult is the contract payload an exchange produced, keyed by contract
// exactly as an external record stores it. An exchange that fills more than one
// contract with one sign-in says so here rather than being called once per
// contract.
type ExchangeResult struct {
	// Secrets maps contract name to the credential that contract declares.
	Secrets map[string]string
	// Values maps contract name to the non-secret values the exchange could
	// answer while it was signed in, keyed as an external record stores them.
	// An app's own account name is a fact about the install, and a remote
	// install's facts are not the ones the catalog's static defaults describe.
	Values map[string]map[string]string
}

var (
	exchangeMu sync.RWMutex
	exchanges  = map[string]CredentialExchange{}
)

// RegisterCredentialExchange records the exchange for one catalog app. App
// packages call it from their init() (see each app's registration.go), so the
// knowledge of how to trade a login for a key lives beside the code that knows
// that app's API and nowhere else. Re-registering an app replaces the previous
// exchange.
func RegisterCredentialExchange(catalogID string, exchange CredentialExchange) {
	exchangeMu.Lock()
	defer exchangeMu.Unlock()
	exchanges[catalogID] = exchange
}

// LookupCredentialExchange returns the registered exchange for a catalog app.
func LookupCredentialExchange(catalogID string) (CredentialExchange, bool) {
	exchangeMu.RLock()
	defer exchangeMu.RUnlock()
	exchange, ok := exchanges[catalogID]
	return exchange, ok
}

// CredentialExchangeNames lists the catalog apps with a registered exchange,
// sorted. It exists so a test can assert every registered exchange still names
// a contract its app provides: an exchange that outlives the offer it feeds is
// a form that stops asking for anything at all.
func CredentialExchangeNames() []string {
	exchangeMu.RLock()
	defer exchangeMu.RUnlock()
	names := make([]string, 0, len(exchanges))
	for name := range exchanges {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
