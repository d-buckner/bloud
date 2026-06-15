package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLDAPOutput(t *testing.T) {
	cfg := &Config{
		LDAPHost:         "ldap.internal",
		LDAPBindPassword: "secret",
	}

	output := cfg.LDAPOutput()

	require.NotNil(t, output)
	assert.Equal(t, "ldap.internal", output.Host)
	assert.Equal(t, 3389, output.Port)
	assert.Equal(t, "dc=ldap,dc=goauthentik,dc=io", output.BaseDN)
	assert.Equal(t, "cn=ldap-service,ou=users,dc=ldap,dc=goauthentik,dc=io", output.BindUser)
	assert.Equal(t, "secret", output.BindPassword)
}

func TestLDAPOutputWithoutPassword(t *testing.T) {
	assert.Nil(t, (&Config{LDAPHost: "ldap.internal"}).LDAPOutput())
}
