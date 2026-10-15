// SPDX-License-Identifier: AGPL-3.0-only

package configurator

import "sync"

// LDAPOutpostTokenVar is the template name the LDAP outpost container reads
// its auth token from ({{authentikLdapToken}}).
const LDAPOutpostTokenVar = "authentikLdapToken"

// TemplateVars holds the variables a container-spec template renders with,
// beyond the {{dataDir}} and {{appDataDir}} the renderer supplies itself.
//
// It replaces a map[string]string that the entry point shared by reference with
// both the authentik configurator and the orchestrator. Authentik's PostStart
// wrote the LDAP outpost token into that map while the orchestrator read it at
// container-spec render time, with no lock and no stated ordering: it held only
// because dependsOn happened to put the write before the read. A configurator
// mutating a value another goroutine is iterating is a data race regardless of
// whether the schedule currently avoids one.
//
// The store makes the one runtime-mutated variable explicit and guards it. The
// static values are copied in at construction and never written again, so
// Snapshot can hand the renderer a private copy and no caller can reach
// through to mutate what someone else is reading.
type TemplateVars struct {
	mu     sync.RWMutex
	static map[string]string

	// ldapOutpostToken is written by the authentik configurator once the
	// identity provider has issued the LDAP outpost's token. Empty until then.
	ldapOutpostToken string
}

// NewTemplateVars builds the store from the static variables. The input map is
// copied, so the caller may reuse or mutate its own map afterwards without
// affecting what the store renders.
func NewTemplateVars(static map[string]string) *TemplateVars {
	copied := make(map[string]string, len(static)+1)
	for k, v := range static {
		copied[k] = v
	}
	return &TemplateVars{static: copied}
}

// SetLDAPOutpostToken records the token the identity provider issued for the
// LDAP outpost container. Safe to call concurrently with Resolve and Snapshot,
// and idempotent: re-setting the same value is a no-op by construction.
func (v *TemplateVars) SetLDAPOutpostToken(token string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.ldapOutpostToken = token
}

// LDAPOutpostToken returns the token, empty while it has not been issued.
func (v *TemplateVars) LDAPOutpostToken() string {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.ldapOutpostToken
}

// Resolve looks up one variable by template name. The second return reports
// whether the name is known at all.
func (v *TemplateVars) Resolve(name string) (string, bool) {
	if name == LDAPOutpostTokenVar {
		token := v.LDAPOutpostToken()
		return token, token != ""
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	value, ok := v.static[name]
	return value, ok
}

// Snapshot returns every variable as it stands right now, including the LDAP
// outpost token when it has been issued. The renderer iterates this copy, so
// nothing it is walking can change underneath it.
//
// A nil store snapshots to an empty map rather than panicking: a build with no
// template variables configured is legitimate, and the renderer passes
// whatever it holds straight through.
func (v *TemplateVars) Snapshot() map[string]string {
	if v == nil {
		return map[string]string{}
	}
	v.mu.RLock()
	out := make(map[string]string, len(v.static)+1)
	for k, val := range v.static {
		out[k] = val
	}
	v.mu.RUnlock()
	if token := v.LDAPOutpostToken(); token != "" {
		out[LDAPOutpostTokenVar] = token
	}
	return out
}
