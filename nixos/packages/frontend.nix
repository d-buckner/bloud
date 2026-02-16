{ pkgs }:

pkgs.buildNpmPackage {
  pname = "bloud-frontend";
  version = "0.1.0";

  src = builtins.path {
    path = ../..;
    name = "bloud-frontend-src";
    filter = path: type:
      let
        baseName = builtins.baseNameOf path;
      in
      baseName != ".git"
      && baseName != "node_modules"
      && baseName != "result"
      && baseName != "result-iso"
      && baseName != ".direnv"
      && baseName != "cli"
      && baseName != "lima"
      && baseName != "integration"
      && baseName != "apps"
      && baseName != "nixos"
      && baseName != "tests"
      && baseName != "scripts";
  };

  npmDepsHash = "sha256-m9yD1u+W8O/2g2Pakhd5dc1yYsDTf1rbXur/WQxq8/8=";

  # Build the frontend workspace
  buildPhase = ''
    runHook preBuild
    npm run build --workspace=services/host-agent/web
    runHook postBuild
  '';

  # No npm install step (all output is static files)
  dontNpmInstall = true;

  installPhase = ''
    runHook preInstall
    mkdir -p $out
    cp -r services/host-agent/web/build/* $out/
    runHook postInstall
  '';

  meta = {
    description = "Bloud web frontend (SvelteKit SPA)";
  };
}
