{ pkgs, lib, ... }:

# Helper function to create a systemd user service for a podman container
# This abstracts the common patterns for running containers with rootless podman
#
# Parameters:
#   waitFor - list of {container, command} to health check before starting
#             e.g. [{ container = "postgres"; command = "pg_isready -U user"; }]
#   bloudAppName - if set, runs bloud-agent configure prestart/poststart hooks
#   bloudAgentPath - path to bloud-agent binary (required if bloudAppName is set)

{ name, image, ports ? [], environment ? {}, volumes ? [], network ? null, dependsOn ? [], cmd ? [], userns ? null, waitFor ? [], extraAfter ? [], extraRequires ? [], bloudAppName ? null, bloudAgentPath ? null }:
let
  # Generate health check script for each waitFor entry
  mkHealthCheck = { container, command, timeout ? 60 }: ''
    echo "Waiting for ${container} to be ready..."
    for i in $(seq 1 ${toString timeout}); do
      if ${pkgs.podman}/bin/podman exec ${container} ${command} >/dev/null 2>&1; then
        echo "${container} is ready"
        break
      fi
      if [ $i -eq ${toString timeout} ]; then
        echo "${container} not ready after ${toString timeout}s, giving up"
        exit 1
      fi
      sleep 1
    done
  '';
  healthCheckScript = lib.concatMapStrings mkHealthCheck waitFor;

  # Bloud configurator scripts (prestart runs before container, poststart after)
  hasConfigurator = bloudAppName != null && bloudAgentPath != null;
  prestartScript = pkgs.writeShellScript "${name}-bloud-prestart" ''
    echo "Running bloud prestart for ${if bloudAppName != null then bloudAppName else name}..."
    ${if bloudAgentPath != null then bloudAgentPath else "/tmp/host-agent"} configure prestart ${if bloudAppName != null then bloudAppName else name}
  '';
  poststartScript = pkgs.writeShellScript "${name}-bloud-poststart" ''
    echo "Running bloud poststart for ${if bloudAppName != null then bloudAppName else name}..."
    ${if bloudAgentPath != null then bloudAgentPath else "/tmp/host-agent"} configure poststart ${if bloudAppName != null then bloudAppName else name}
  '';
in
{
  description = "Podman container: ${name}";
  after = [ "network-online.target" ] ++ (map (dep: "podman-${dep}.service") dependsOn) ++ extraAfter;
  wants = [ "network-online.target" ] ++ (map (dep: "podman-${dep}.service") dependsOn);
  requires = extraRequires;
  wantedBy = [ "bloud-apps.target" ];

  # Add /run/wrappers/bin to PATH for newuidmap/newgidmap (rootless podman)
  # Also add podman to PATH so configurators can use it
  path = [ "/run/wrappers" pkgs.podman ];

  serviceConfig = {
    Type = "notify";
    NotifyAccess = "all";
    Restart = "always";
    TimeoutStartSec = 900;

    ExecStartPre = [
      "-${pkgs.podman}/bin/podman rm -f ${name}"
    ] ++ lib.optional (waitFor != []) (pkgs.writeShellScript "${name}-wait-for-deps" healthCheckScript)
      ++ lib.optional hasConfigurator prestartScript;

    ExecStart =
      let
        portArgs = lib.concatMapStrings (p: " -p ${p}") ports;
        envArgs = lib.concatStrings (lib.mapAttrsToList (k: v: " -e ${k}=${lib.escapeShellArg v}") environment);
        volArgs = lib.concatMapStrings (v: " -v ${v}") volumes;
        netArg = if network != null then " --network=${network}" else "";
        usernsArg = if userns != null then " --userns=${userns}" else "";
        cmdArgs = lib.concatMapStrings (c: " ${lib.escapeShellArg c}") cmd;
      in
      "${pkgs.podman}/bin/podman run --pull=missing --sdnotify=conmon --name=${name} --rm${portArgs}${envArgs}${volArgs}${netArg}${usernsArg} ${image}${cmdArgs}";

    ExecStartPost = lib.optional hasConfigurator poststartScript;

    ExecStop = "${pkgs.podman}/bin/podman stop -t 10 ${name}";
  };
}
