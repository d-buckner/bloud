// SPDX-License-Identifier: AGPL-3.0-only

package seerr

import (
	"context"
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// credentialExchange trades a sign-in for the API key the requestManager
// contract declares, for a Seerr Bloud does not boot.
//
// The local install never needs it: the configurator reads main.apiKey out of
// settings.json after boot and publishes it, and nobody types anything. A remote
// Seerr has no filesystem Bloud can read, so the same value has to come back over
// the network, from the one account that can see it.
//
// What makes this worth an interface instead of a second input box is what a
// Seerr admin account *is*. Seerr ships no local password: its admin is the
// Jellyfin account that completed the first-run wizard, and a Bloud-shaped Seerr
// was onboarded with the Jellyfin login Bloud itself minted. So the credential the
// operator already holds is a username and password, and in the common case Bloud
// holds that pair too (mediaServer publishes it), which means the form can ask
// for nothing at all and still complete the trade.
type credentialExchange struct{}

// exchangeInputs are the two fields the form shows when Bloud holds no login.
// There is no third field for the API key on purpose: the exchange replaces it,
// and a form that offered both would let a pasted key be silently overwritten by
// the login sitting next to it.
var exchangeInputs = []configurator.ExchangeInput{
	{
		Key:   configurator.ExchangeInputUsername,
		Label: "Username",
		Help:  "An admin account on that Seerr. On a Seerr that was set up against Jellyfin, this is the Jellyfin username.",
	},
	{
		Key:    configurator.ExchangeInputPassword,
		Label:  "Password",
		Secret: true,
		Help:   "That account's password. It is sent to the Seerr address above, which signs in to its own Jellyfin; it is never stored.",
	},
}

func (credentialExchange) Contract() string                     { return contractRequestManager }
func (credentialExchange) Inputs() []configurator.ExchangeInput { return exchangeInputs }

// LoginContract is mediaServer: the account that can sign in to a Seerr is the
// media server account it was onboarded with, and Bloud publishes that login for
// whoever fills the role. Empty when nothing here does.
func (credentialExchange) LoginContract() string { return contractMediaServer }

// Exchange signs in and reads the key. It writes no settings to the remote
// instance: the one thing an operator must never have to worry about is Bloud
// rotating a credential their other integrations are already using, so this never
// calls /settings/main/regenerate. A sign-in itself can create the Seerr account
// for a Jellyfin user that has none, which is what signing in means and is not
// something this flow can avoid.
func (e credentialExchange) Exchange(ctx context.Context, req configurator.ExchangeRequest) (configurator.ExchangeResult, error) {
	login, ok := req.Chosen()
	if !ok {
		return configurator.ExchangeResult{}, fmt.Errorf(
			"a username and password are both needed to sign in to %s", req.Endpoint)
	}

	api := newAPI(req.HTTP, func() string { return req.Endpoint })

	// An uninitialized Seerr has no Jellyfin configured, so the sign-in below
	// would fail with a message about a hostname and the operator would have no
	// way to know the instance is simply still in its own wizard.
	initialized, err := api.initialized(ctx)
	if err != nil {
		return configurator.ExchangeResult{}, fmt.Errorf("cannot reach that Seerr: %w", err)
	}
	if !initialized {
		return configurator.ExchangeResult{}, fmt.Errorf(
			"that Seerr has not finished its own setup. Open it once in a browser and complete its first-run wizard, " +
				"which is where it learns which media server to authenticate against")
	}

	user, err := api.signInAs(ctx, login)
	if err != nil {
		return configurator.ExchangeResult{}, fmt.Errorf("signing in to Seerr as %q failed: %w", login.Username, err)
	}

	apiKey, err := api.mainAPIKey(ctx)
	if err != nil {
		return configurator.ExchangeResult{}, fmt.Errorf("reading Seerr's API key after signing in: %w", err)
	}
	if apiKey == "" {
		// GET /settings/main omits apiKey for anyone who is not an admin, so an
		// empty value here is not a missing field: the sign-in worked and the
		// account cannot see the credential.
		return configurator.ExchangeResult{}, fmt.Errorf(
			"signed in as %s, but that account is not an admin in Seerr, so it cannot see the API key",
			user.displayName())
	}

	return configurator.ExchangeResult{
		Secrets: map[string]string{contractRequestManager: apiKey},
		// defaultUser is the account requests get attributed to. For a Seerr
		// Bloud booted that is the managed account the configurator made; here
		// the honest answer is the account we just signed in as, which is the
		// only one this exchange has any evidence about.
		Values: map[string]map[string]string{
			contractRequestManager: {valueDefaultUser: user.displayName()},
		},
	}, nil
}
