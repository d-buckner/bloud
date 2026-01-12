{ pkgs, lib }:

# Shared helper to provision OAuth2 applications in Authentik
# Usage:
#   let provisionAuthentikApp = import ../lib/authentik-app.nix { inherit pkgs lib; };
#   in provisionAuthentikApp {
#     appName = "My App";
#     slug = "my-app";
#     clientId = "my-app-client";
#     clientSecret = "my-app-secret";
#     redirectUris = [ "http://localhost:8080/callback" ];
#     authentikPort = 9001;  # optional, defaults to 9001
#   }

{ appName
, slug
, clientId
, clientSecret
, redirectUris
, authentikPort ? 9001
, authorizationFlow ? "default-provider-authorization-implicit-consent"
, launchUrl ? null
, apiToken ? null
}:

let
  redirectUrisStr = lib.concatStringsSep "\\n" redirectUris;
  launchUrlConfig = if launchUrl != null then ''"meta_launch_url": "${launchUrl}",'' else "";
in
pkgs.writeShellScript "provision-authentik-${slug}" ''
  set -e

  echo "=== Provisioning ${appName} in Authentik ==="

  # Wait for Authentik to be ready
  echo "Waiting for Authentik to be ready..."
  for i in {1..60}; do
    if ${pkgs.curl}/bin/curl -sf http://localhost:${toString authentikPort}/api/v3/root/config/ &>/dev/null; then
      echo "Authentik is ready!"
      break
    fi
    echo "Waiting for Authentik... ($i/60)"
    sleep 2
  done

  # Get API token
  ${if apiToken != null then ''
    echo "Using provided API token"
    TOKEN="${apiToken}"
  '' else ''
    echo "ERROR: No API token provided"
    echo "Please provide an apiToken parameter (e.g., bootstrap token)"
    exit 1
  ''}

  # Check if provider already exists
  echo "Checking for existing OAuth2 provider..."
  EXISTING_PROVIDER=$(${pkgs.curl}/bin/curl -sf \
    -H "Authorization: Bearer $TOKEN" \
    "http://localhost:${toString authentikPort}/api/v3/providers/oauth2/?name=${appName}+OAuth2+Provider" 2>&1 | \
    ${pkgs.jq}/bin/jq -r '.results[0].pk // empty' 2>/dev/null || echo "")

  if [ -n "$EXISTING_PROVIDER" ]; then
    echo "Provider already exists with ID: $EXISTING_PROVIDER"
    PROVIDER_ID="$EXISTING_PROVIDER"
  else
    echo "Creating new OAuth2 provider..."

    # Get first available certificate for signing
    CERT_UUID=$(${pkgs.curl}/bin/curl -sf \
      -H "Authorization: Bearer $TOKEN" \
      http://localhost:${toString authentikPort}/api/v3/crypto/certificatekeypairs/ 2>&1 | \
      ${pkgs.jq}/bin/jq -r '.results[0].pk // empty' 2>/dev/null || echo "")

    if [ -z "$CERT_UUID" ]; then
      echo "ERROR: Could not retrieve certificate for signing"
      exit 1
    fi

    echo "Using certificate: $CERT_UUID"

    # Create OAuth2 provider
    PROVIDER_RESPONSE=$(${pkgs.curl}/bin/curl -sf -X POST \
      -H "Authorization: Bearer $TOKEN" \
      -H "Content-Type: application/json" \
      http://localhost:${toString authentikPort}/api/v3/providers/oauth2/ \
      -d @- <<EOF
    {
      "name": "${appName} OAuth2 Provider",
      "authorization_flow": "${authorizationFlow}",
      "client_type": "confidential",
      "client_id": "${clientId}",
      "client_secret": "${clientSecret}",
      "redirect_uris": "${redirectUrisStr}",
      "signing_key": "$CERT_UUID",
      "sub_mode": "user_username"
    }
EOF
    )

    PROVIDER_ID=$(echo "$PROVIDER_RESPONSE" | ${pkgs.jq}/bin/jq -r '.pk // empty' 2>/dev/null || echo "")

    if [ -z "$PROVIDER_ID" ]; then
      echo "ERROR: Failed to create provider"
      echo "Response: $PROVIDER_RESPONSE"
      exit 1
    fi

    echo "Provider created with ID: $PROVIDER_ID"
  fi

  # Check if application exists
  echo "Checking for existing application..."
  EXISTING_APP=$(${pkgs.curl}/bin/curl -sf \
    -H "Authorization: Bearer $TOKEN" \
    "http://localhost:${toString authentikPort}/api/v3/core/applications/?slug=${slug}" 2>&1 | \
    ${pkgs.jq}/bin/jq -r '.results[0].pk // empty' 2>/dev/null || echo "")

  if [ -n "$EXISTING_APP" ]; then
    echo "Application already exists"
  else
    echo "Creating application..."
    APP_RESPONSE=$(${pkgs.curl}/bin/curl -sf -X POST \
      -H "Authorization: Bearer $TOKEN" \
      -H "Content-Type: application/json" \
      http://localhost:${toString authentikPort}/api/v3/core/applications/ \
      -d @- <<EOF
    {
      "name": "${appName}",
      "slug": "${slug}",
      "provider": $PROVIDER_ID,
      ${launchUrlConfig}
      "policy_engine_mode": "any"
    }
EOF
    )

    APP_ID=$(echo "$APP_RESPONSE" | ${pkgs.jq}/bin/jq -r '.pk // empty' 2>/dev/null || echo "")

    if [ -n "$APP_ID" ]; then
      echo "Application created successfully!"
    else
      echo "WARNING: Application creation may have failed"
      echo "Response: $APP_RESPONSE"
    fi
  fi

  echo "=== ${appName} provisioning complete! ==="
''
