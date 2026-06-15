package config

import "codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"

const (
	defaultLDAPPort     = 3389
	defaultLDAPBaseDN   = "dc=ldap,dc=goauthentik,dc=io"
	defaultLDAPBindUser = "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io"
)

// LDAPOutput returns the typed LDAP provider output exposed to configurators.
func (c *Config) LDAPOutput() *configurator.LDAPOutput {
	if c.LDAPBindPassword == "" {
		return nil
	}
	return &configurator.LDAPOutput{
		Host:         c.LDAPHost,
		Port:         defaultLDAPPort,
		BaseDN:       defaultLDAPBaseDN,
		BindUser:     defaultLDAPBindUser,
		BindPassword: c.LDAPBindPassword,
	}
}
