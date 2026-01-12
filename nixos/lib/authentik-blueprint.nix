{ lib }:

# Helper to generate Authentik blueprint YAML for OAuth2 applications
# Blueprints are automatically loaded by Authentik on startup - no API calls needed!
#
# Usage:
#   let mkAuthentikBlueprint = import ../lib/authentik-blueprint.nix { inherit lib; };
#   in mkAuthentikBlueprint {
#     appName = "My App";
#     slug = "my-app";
#     clientId = "my-app-client";
#     clientSecret = "my-app-secret";
#     redirectUris = [ "http://localhost:8080/callback" ];
#   }

{ appName
, slug
, clientId
, clientSecret
, redirectUris
, authorizationFlow ? "default-provider-authorization-implicit-consent"
, launchUrl ? null
}:

let
  # Format redirect URIs as YAML list with url and matching_mode (Authentik 2024+ format)
  # Each entry needs 8 spaces for proper YAML indentation under attrs.redirect_uris
  formatRedirectUri = uri: "        - url: \"${uri}\"\n          matching_mode: strict";
  redirectUrisYaml = lib.concatStringsSep "\n" (map formatRedirectUri redirectUris);

  # Optional launch URL (6 spaces to match attrs level - same as group:)
  launchUrlLine = if launchUrl != null then "\n      meta_launch_url: \"${launchUrl}\"" else "";
in
''
# Authentik Blueprint for ${appName}
# This is automatically loaded by Authentik on startup
version: 1
metadata:
  name: ${slug}-sso-blueprint
  labels:
    managed-by: bloud

entries:
  # OAuth2 Provider
  - model: authentik_providers_oauth2.oauth2provider
    id: ${slug}-oauth2-provider
    identifiers:
      name: ${appName} OAuth2 Provider
    attrs:
      authorization_flow: !Find [authentik_flows.flow, [slug, ${authorizationFlow}]]
      invalidation_flow: !Find [authentik_flows.flow, [slug, default-provider-invalidation-flow]]
      client_type: confidential
      client_id: ${clientId}
      client_secret: ${clientSecret}
      redirect_uris:
${redirectUrisYaml}
      signing_key: !Find [authentik_crypto.certificatekeypair, [name, "authentik Self-signed Certificate"]]
      sub_mode: hashed_user_id
      include_claims_in_id_token: true
      access_code_validity: minutes=1
      access_token_validity: minutes=5
      refresh_token_validity: days=30
      # Property mappings for OIDC scopes (required for userinfo endpoint)
      property_mappings:
        - !Find [authentik_providers_oauth2.scopemapping, [managed, goauthentik.io/providers/oauth2/scope-openid]]
        - !Find [authentik_providers_oauth2.scopemapping, [managed, goauthentik.io/providers/oauth2/scope-email]]
        - !Find [authentik_providers_oauth2.scopemapping, [managed, goauthentik.io/providers/oauth2/scope-profile]]

  # Application
  - model: authentik_core.application
    id: ${slug}-application
    identifiers:
      slug: ${slug}
    attrs:
      name: ${appName}
      provider: !KeyOf ${slug}-oauth2-provider
      policy_engine_mode: any
      group: ""${launchUrlLine}
''
