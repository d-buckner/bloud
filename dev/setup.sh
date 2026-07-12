#!/bin/bash
# One-time bootstrap for the Bloud dev environment inside the Lima VM.
#
# Usage (from inside the Lima VM):
#   limactl shell bloud-dev bash dev/setup.sh
#
# What it does:
#   1. Creates data directory structure
#   2. Builds host-agent and runs init-secrets
#   3. Downloads + installs Jellyfin LDAP plugin
#   4. Copies Authentik blueprints and branding assets
#   5. Generates .env file for compose from secrets.json

set -euo pipefail

# Derive BLOUD_DIR from this script's location (works regardless of mount path)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BLOUD_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DATA_DIR="${HOME}/.local/share/bloud"

echo "==> Configuring podman..."
mkdir -p ~/.config/containers
cat > ~/.config/containers/containers.conf << 'CONF'
[engine]
cgroup_manager = "cgroupfs"

[containers]
log_driver = "k8s-file"
CONF

echo "==> Creating data directories..."
mkdir -p \
  "${DATA_DIR}/authentik/media" \
  "${DATA_DIR}/authentik/templates" \
  "${DATA_DIR}/authentik/blueprints" \
  "${DATA_DIR}/authentik/branding" \
  "${DATA_DIR}/jellyfin/config/plugins/LDAP-Auth" \
  "${DATA_DIR}/jellyfin/cache" \
  "${DATA_DIR}/media/movies" \
  "${DATA_DIR}/media/shows" \
  "${DATA_DIR}/postgres" \
  "${DATA_DIR}/redis"

# Authentik containers run as non-root and need write access to media/templates
chmod 777 "${DATA_DIR}/authentik/media" "${DATA_DIR}/authentik/templates"

echo "==> Building host-agent..."
cd "${BLOUD_DIR}/services/host-agent"
go build -o /tmp/host-agent ./cmd/host-agent

echo "==> Initializing secrets..."
BLOUD_DATA_DIR="${DATA_DIR}" /tmp/host-agent init-secrets

echo "==> Installing Jellyfin LDAP plugin..."
LDAP_PLUGIN_DIR="${DATA_DIR}/jellyfin/config/plugins/LDAP-Auth"
if [ -f "${LDAP_PLUGIN_DIR}/LDAP-Auth.dll" ]; then
  echo "    LDAP plugin already installed"
else
  LDAP_ZIP="/tmp/ldap-auth-plugin.zip"
  curl -sSL -o "${LDAP_ZIP}" \
    "https://repo.jellyfin.org/files/plugin/ldap-authentication/ldap-authentication_23.0.0.0.zip"
  unzip -o "${LDAP_ZIP}" -d "${LDAP_PLUGIN_DIR}"
  rm -f "${LDAP_ZIP}"
  echo "    LDAP plugin installed"
fi

echo "==> Copying Authentik blueprints..."
cp "${BLOUD_DIR}/apps/authentik/branding/bloud-brand.yaml" \
   "${DATA_DIR}/authentik/blueprints/bloud-brand.yaml"
cp "${BLOUD_DIR}/apps/authentik/auth.yaml" \
   "${DATA_DIR}/authentik/blueprints/flow-default-authentication-flow.yaml"

echo "==> Copying Authentik branding assets..."
cp "${BLOUD_DIR}/apps/authentik/branding/logo.svg" \
   "${DATA_DIR}/authentik/branding/logo.svg"
cp "${BLOUD_DIR}/apps/authentik/branding/favicon.svg" \
   "${DATA_DIR}/authentik/branding/favicon.svg"

echo "==> Generating compose .env from secrets.json..."
SECRETS_FILE="${DATA_DIR}/secrets.json"
if [ -f "${SECRETS_FILE}" ]; then
  POSTGRES_PASSWORD=$(jq -r '.postgresPassword' "${SECRETS_FILE}")
  AUTHENTIK_SECRET_KEY=$(jq -r '.authentikSecretKey' "${SECRETS_FILE}")
  AUTHENTIK_BOOTSTRAP_PASSWORD=$(jq -r '.authentikBootstrapPassword' "${SECRETS_FILE}")
  AUTHENTIK_BOOTSTRAP_TOKEN=$(jq -r '.authentikBootstrapToken' "${SECRETS_FILE}")

  LDAP_BIND_PASSWORD=$(jq -r '.ldapBindPassword' "${SECRETS_FILE}")

  cat > "${BLOUD_DIR}/dev/.env" <<EOF
POSTGRES_PASSWORD=${POSTGRES_PASSWORD}
AUTHENTIK_SECRET_KEY=${AUTHENTIK_SECRET_KEY}
AUTHENTIK_BOOTSTRAP_PASSWORD=${AUTHENTIK_BOOTSTRAP_PASSWORD}
AUTHENTIK_BOOTSTRAP_TOKEN=${AUTHENTIK_BOOTSTRAP_TOKEN}
BLOUD_DATA=${DATA_DIR}
# Populated after authentik poststart creates the LDAP outpost
AUTHENTIK_LDAP_TOKEN=placeholder
EOF

  # Also write a host-agent env file for running configure commands
  cat > "${BLOUD_DIR}/dev/host-agent.env" <<EOF
BLOUD_DATA_DIR=${DATA_DIR}
BLOUD_APPS_DIR=${BLOUD_DIR}/apps
DATABASE_URL=postgres://apps:${POSTGRES_PASSWORD}@localhost:5432/bloud?sslmode=disable
BLOUD_LDAP_BIND_PASSWORD=${LDAP_BIND_PASSWORD}
BLOUD_LDAP_HOST=apps-authentik-ldap
BLOUD_AUTHENTIK_PORT=9000
EOF
  chmod 600 "${BLOUD_DIR}/dev/.env" "${BLOUD_DIR}/dev/host-agent.env"
  echo "    .env written"
else
  echo "    WARNING: secrets.json not found, compose will use defaults"
fi

echo ""
echo "==> Setup complete!"
echo ""
echo "Next steps:"
echo "  cd ${BLOUD_DIR}/dev && podman-compose up -d"
echo "  # Wait for services to be healthy, then run configurators:"
echo "  set -a && source ${BLOUD_DIR}/dev/host-agent.env && set +a"
echo "  /tmp/host-agent configure poststart authentik"
echo "  /tmp/host-agent configure poststart jellyfin"
