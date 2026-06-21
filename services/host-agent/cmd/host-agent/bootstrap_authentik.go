package main

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	authentikApp "codeberg.org/d-buckner/bloud-v3/apps/authentik"
	containerruntime "codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/container"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/internal/config"
	authentikClient "codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/authentik"
	"codeberg.org/d-buckner/bloud-v3/services/host-agent/pkg/configurator"
)

//go:embed scripts/seed_dev_user.py
var seedDevUserScript string

const (
	authentikImage     = "ghcr.io/goauthentik/server:2025.10.3"
	authentikLDAPImage = "ghcr.io/goauthentik/ldap:2025.10.3"
)

// bootstrapAuthentik ensures the Authentik SSO stack (server, worker, LDAP outpost)
// is running and configured. It reuses the existing Authentik configurator for
// health checking and PostStart setup (admin password, API token, LDAP infrastructure).
func bootstrapAuthentik(cfg *config.Config, runtime containerruntime.Runtime, logger *slog.Logger) error {
	ctx := context.Background()

	// Create authentik database in the shared postgres instance.
	logger.Info("ensuring authentik database exists")
	if err := ensureAuthentikDB(cfg.DatabaseURL); err != nil {
		return fmt.Errorf("create authentik database: %w", err)
	}

	// Copy the custom auth flow blueprint (username-only identification).
	if err := prepareAuthentikBlueprints(cfg); err != nil {
		return fmt.Errorf("prepare blueprints: %w", err)
	}

	dataDir := cfg.DataDir
	authFlowPath := filepath.Join(dataDir, "authentik-auth-flow.yaml")

	// Create mount directories. Authentik runs as a non-root user inside the
	// container. With rootless podman, that UID maps to a high host UID that
	// can't write to 0755 directories, so media/templates need 0777.
	for _, dir := range []string{
		filepath.Join(dataDir, "authentik"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	for _, dir := range []string{
		filepath.Join(dataDir, "authentik-media"),
		filepath.Join(dataDir, "authentik-templates"),
	} {
		if err := os.MkdirAll(dir, 0777); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}

	if err := runtime.EnsureNetwork(ctx, "apps-net"); err != nil {
		return fmt.Errorf("ensure network: %w", err)
	}

	serverEnv, workerEnv := authentikEnvVars(cfg)
	sharedMounts := []containerruntime.Mount{
		{Source: filepath.Join(dataDir, "authentik-media"), Destination: "/media"},
		{Source: filepath.Join(dataDir, "authentik-templates"), Destination: "/templates"},
		{Source: authFlowPath, Destination: "/blueprints/default/flow-default-authentication-flow.yaml", Options: []string{"ro"}},
	}

	// Start authentik-server.
	logger.Info("bootstrapping authentik server")
	if _, err := runtime.Ensure(ctx, containerruntime.Spec{
		Name:          "apps-authentik-server",
		Image:         authentikImage,
		Command:       []string{"server"},
		Environment:   serverEnv,
		Network:       "apps-net",
		RestartPolicy: "always",
		Ports:         []containerruntime.Port{{Host: 9001, Container: 9000}},
		Mounts:        sharedMounts,
		Labels:        map[string]string{"io.bloud.app": "authentik"},
	}); err != nil {
		return fmt.Errorf("ensure authentik-server: %w", err)
	}

	// Start authentik-worker (processes blueprints and background tasks).
	logger.Info("bootstrapping authentik worker")
	if _, err := runtime.Ensure(ctx, containerruntime.Spec{
		Name:          "apps-authentik-worker",
		Image:         authentikImage,
		Command:       []string{"worker"},
		Environment:   workerEnv,
		Network:       "apps-net",
		RestartPolicy: "always",
		Mounts:        sharedMounts,
		Labels:        map[string]string{"io.bloud.app": "authentik"},
	}); err != nil {
		return fmt.Errorf("ensure authentik-worker: %w", err)
	}

	// Run the existing Authentik configurator: health check, then PostStart
	// (sets admin password, creates API token, configures login page, creates LDAP infra).
	// On first boot this waits for DB migrations which can take several minutes.
	logger.Info("waiting for authentik to be healthy (first boot may take several minutes)")
	appDataPath := filepath.Join(dataDir, "authentik")
	cfgr := authentikApp.NewConfigurator(
		cfg.AuthentikPort,
		cfg.AuthentikAdminPassword,
		cfg.AuthentikAdminEmail,
		cfg.AuthentikToken,
		cfg.LDAPBindPassword,
		appDataPath,
		"", // skip branding CSS in dev
	)
	if err := cfgr.HealthCheck(ctx); err != nil {
		return fmt.Errorf("authentik health check: %w", err)
	}
	logger.Info("authentik is healthy, running configuration")
	state := &configurator.AppState{
		DataPath:      appDataPath,
		BloudDataPath: dataDir,
	}
	if err := cfgr.PostStart(ctx, state); err != nil {
		return fmt.Errorf("authentik poststart: %w", err)
	}
	logger.Info("authentik configured")

	// Retrieve the auto-generated LDAP outpost token and start the LDAP container.
	client := authentikClient.NewClient(
		fmt.Sprintf("http://localhost:%d", cfg.AuthentikPort),
		cfg.AuthentikToken,
	)
	ldapToken, err := client.GetLDAPOutpostToken()
	if err != nil {
		return fmt.Errorf("get LDAP outpost token: %w", err)
	}

	logger.Info("starting authentik LDAP outpost")
	if _, err := runtime.Ensure(ctx, containerruntime.Spec{
		Name:    "apps-authentik-ldap",
		Image:   authentikLDAPImage,
		Network: "apps-net",
		Environment: map[string]string{
			"AUTHENTIK_HOST":  "http://apps-authentik-server:9000",
			"AUTHENTIK_TOKEN": ldapToken,
		},
		RestartPolicy: "always",
		Ports: []containerruntime.Port{
			{Host: 3389, Container: 3389},
			{Host: 6636, Container: 6636},
		},
		Labels: map[string]string{"io.bloud.app": "authentik"},
	}); err != nil {
		return fmt.Errorf("ensure authentik-ldap: %w", err)
	}
	logger.Info("authentik LDAP outpost started")

	// Seed a dev "admin" user so Jellyfin LDAP login works with admin/password.
	if err := seedAuthentikDevUser(ctx, logger); err != nil {
		logger.Warn("failed to seed dev user in authentik", "error", err)
	}

	return nil
}

// authentikEnvVars builds the environment maps for the server and worker containers.
// Server gets bootstrap credentials; worker only needs core connection vars.
func authentikEnvVars(cfg *config.Config) (serverEnv, workerEnv map[string]string) {
	common := map[string]string{
		"AUTHENTIK_SECRET_KEY":               cfg.Secrets.GetAuthentikSecretKey(),
		"AUTHENTIK_POSTGRESQL__HOST":         "apps-postgres",
		"AUTHENTIK_POSTGRESQL__PORT":         "5432",
		"AUTHENTIK_POSTGRESQL__USER":         "apps",
		"AUTHENTIK_POSTGRESQL__NAME":         "authentik",
		"AUTHENTIK_POSTGRESQL__PASSWORD":     cfg.PostgresPassword,
		"AUTHENTIK_REDIS__HOST":              "redis://apps-redis:6379",
		"AUTHENTIK_ERROR_REPORTING__ENABLED": "false",
	}

	serverEnv = make(map[string]string, len(common)+4)
	for k, v := range common {
		serverEnv[k] = v
	}
	serverEnv["AUTHENTIK_BOOTSTRAP_PASSWORD"] = cfg.AuthentikAdminPassword
	serverEnv["AUTHENTIK_BOOTSTRAP_EMAIL"] = cfg.AuthentikAdminEmail
	serverEnv["AUTHENTIK_BOOTSTRAP_TOKEN"] = cfg.Secrets.GetAuthentikBootstrapToken()
	serverEnv["AUTHENTIK_BLUEPRINTS_DIR"] = "/blueprints"

	workerEnv = make(map[string]string, len(common))
	for k, v := range common {
		workerEnv[k] = v
	}

	return serverEnv, workerEnv
}

// ensureAuthentikDB creates the "authentik" database in the shared postgres instance
// if it doesn't already exist. Authentik manages its own schema via Django migrations.
func ensureAuthentikDB(databaseURL string) error {
	conn, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return err
	}
	defer conn.Close()

	var exists bool
	if err := conn.QueryRow("SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = 'authentik')").Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = conn.Exec("CREATE DATABASE authentik OWNER apps")
	return err
}

// prepareAuthentikBlueprints copies the custom auth flow blueprint to the data directory
// so it can be mounted into the Authentik containers.
func prepareAuthentikBlueprints(cfg *config.Config) error {
	srcPath := filepath.Join(cfg.AppsDir, "authentik", "auth.yaml")
	dstPath := filepath.Join(cfg.DataDir, "authentik-auth-flow.yaml")

	src, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read auth.yaml: %w", err)
	}

	return os.WriteFile(dstPath, src, 0644)
}

// seedAuthentikDevUser creates an "admin" user with password "password" in Authentik
// and adds them to the authentik Admins group. This enables Jellyfin LDAP login
// with admin/password in dev mode.
func seedAuthentikDevUser(ctx context.Context, logger *slog.Logger) error {
	args := []string{
		"exec", "-e", "BLOUD_DEV_PASSWORD=password",
		"apps-authentik-server", "ak", "shell", "-c", seedDevUserScript,
	}
	output, err := exec.CommandContext(ctx, "podman", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("django shell failed: %w (output: %s)", err, string(output))
	}

	result := strings.TrimSpace(string(output))
	if !strings.Contains(result, "OK") {
		return fmt.Errorf("unexpected output: %s", result)
	}

	logger.Info("seeded dev user in authentik", "output", result)
	return nil
}
