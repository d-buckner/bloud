// SPDX-License-Identifier: AGPL-3.0-only

package jellyfin

import (
	"context"
	"encoding/json"
	"fmt"

	"codeberg.org/d-buckner/bloud/services/host-agent/pkg/configurator"
)

// LDAPConfig represents the LDAP plugin configuration.
type LDAPConfig struct {
	LdapServer                     string   `json:"LdapServer"`
	LdapPort                       int      `json:"LdapPort"`
	UseSsl                         bool     `json:"UseSsl"`
	UseStartTls                    bool     `json:"UseStartTls"`
	SkipSslVerify                  bool     `json:"SkipSslVerify"`
	LdapBindUser                   string   `json:"LdapBindUser"`
	LdapBindPassword               string   `json:"LdapBindPassword"`
	LdapBaseDn                     string   `json:"LdapBaseDn"`
	LdapSearchFilter               string   `json:"LdapSearchFilter"`
	LdapAdminBaseDn                string   `json:"LdapAdminBaseDn"`
	LdapAdminFilter                string   `json:"LdapAdminFilter"`
	EnableLdapAdminFilterMemberUid bool     `json:"EnableLdapAdminFilterMemberUid"`
	LdapSearchAttributes           string   `json:"LdapSearchAttributes"`
	LdapClientCertPath             string   `json:"LdapClientCertPath"`
	LdapClientKeyPath              string   `json:"LdapClientKeyPath"`
	LdapRootCaPath                 string   `json:"LdapRootCaPath"`
	CreateUsersFromLdap            bool     `json:"CreateUsersFromLdap"`
	AllowPassChange                bool     `json:"AllowPassChange"`
	LdapUidAttribute               string   `json:"LdapUidAttribute"`
	LdapUsernameAttribute          string   `json:"LdapUsernameAttribute"`
	LdapPasswordAttribute          string   `json:"LdapPasswordAttribute"`
	EnableLdapProfileImageSync     bool     `json:"EnableLdapProfileImageSync"`
	RemoveImagesNotInLdap          bool     `json:"RemoveImagesNotInLdap"`
	LdapProfileImageAttribute      string   `json:"LdapProfileImageAttribute"`
	EnableAllFolders               bool     `json:"EnableAllFolders"`
	EnabledFolders                 []string `json:"EnabledFolders"`
	PasswordResetUrl               string   `json:"PasswordResetUrl"`
}

// configureLDAP configures the LDAP plugin using the typed LDAP output from
// AppState. It is a compare-and-apply: read current, skip if already converged,
// otherwise POST the desired config.
func (c *Configurator) configureLDAP(ctx context.Context, state *configurator.AppState) error {
	ldap := state.LDAP
	desiredConfig := desiredLDAPConfig(ldap)

	adminPassword, err := c.resolveAdminPassword()
	if err != nil {
		return err
	}
	token, err := c.api.authenticate(ctx, bootstrapUsername, adminPassword)
	if err != nil {
		return fmt.Errorf("authenticating: %w", err)
	}

	// Read the current plugin config; a failure means the plugin isn't installed,
	// which is not an error for LDAP config (we simply can't apply yet).
	currentConfig, err := c.api.getPluginConfiguration(ctx, token, ldapPluginID)
	if err != nil {
		c.logger.Warn("could not get LDAP plugin config (plugin may not be installed)", "error", err)
		return nil
	}

	var config LDAPConfig
	if err := json.Unmarshal(currentConfig, &config); err != nil {
		return fmt.Errorf("parsing LDAP config: %w", err)
	}

	if ldapConfigMatchesDesired(config, desiredConfig) {
		c.logger.Info("LDAP already configured")
		return nil
	}

	c.logger.Info("applying LDAP configuration", "ldap_host", ldap.Host, "ldap_port", ldap.Port, "base_dn", ldap.BaseDN)
	configBytes, err := json.Marshal(desiredConfig)
	if err != nil {
		return fmt.Errorf("marshalling LDAP config: %w", err)
	}
	if err := c.api.setPluginConfiguration(ctx, token, ldapPluginID, configBytes); err != nil {
		return fmt.Errorf("setting LDAP config: %w", err)
	}

	c.logger.Info("LDAP configured successfully")
	return nil
}

func desiredLDAPConfig(ldap *configurator.LDAPOutput) LDAPConfig {
	return LDAPConfig{
		LdapServer:            ldap.Host,
		LdapPort:              ldap.Port,
		UseSsl:                false,
		UseStartTls:           false,
		SkipSslVerify:         true,
		LdapBindUser:          ldap.BindUser,
		LdapBindPassword:      ldap.BindPassword,
		LdapBaseDn:            ldap.BaseDN,
		LdapSearchFilter:      "(objectClass=user)",
		LdapAdminBaseDn:       "",
		LdapAdminFilter:       fmt.Sprintf("(memberOf=cn=authentik Admins,ou=groups,%s)", ldap.BaseDN),
		LdapSearchAttributes:  "uid, cn, mail, displayName, sAMAccountName",
		LdapUidAttribute:      "sAMAccountName",
		LdapUsernameAttribute: "cn",
		LdapPasswordAttribute: "userPassword",
		CreateUsersFromLdap:   true,
		AllowPassChange:       false,
		EnableAllFolders:      true,
		EnabledFolders:        []string{},
	}
}

func ldapConfigMatchesDesired(current, desired LDAPConfig) bool {
	return current.LdapServer == desired.LdapServer &&
		current.LdapPort == desired.LdapPort &&
		current.UseSsl == desired.UseSsl &&
		current.UseStartTls == desired.UseStartTls &&
		current.SkipSslVerify == desired.SkipSslVerify &&
		current.LdapBindUser == desired.LdapBindUser &&
		current.LdapBindPassword == desired.LdapBindPassword &&
		current.LdapBaseDn == desired.LdapBaseDn &&
		current.LdapSearchFilter == desired.LdapSearchFilter &&
		current.LdapAdminBaseDn == desired.LdapAdminBaseDn &&
		current.LdapAdminFilter == desired.LdapAdminFilter &&
		current.LdapSearchAttributes == desired.LdapSearchAttributes &&
		current.LdapUidAttribute == desired.LdapUidAttribute &&
		current.LdapUsernameAttribute == desired.LdapUsernameAttribute &&
		current.LdapPasswordAttribute == desired.LdapPasswordAttribute &&
		current.CreateUsersFromLdap == desired.CreateUsersFromLdap &&
		current.AllowPassChange == desired.AllowPassChange &&
		current.EnableAllFolders == desired.EnableAllFolders
}
