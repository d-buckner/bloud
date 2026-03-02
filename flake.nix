{
  description = "Bloud - Home Cloud Operating System";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";
  };

  outputs = { self, nixpkgs }:
    let
      supportedSystems = [ "aarch64-linux" "x86_64-linux" ];

      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
    in
    {
      # NixOS configurations
      nixosConfigurations = {
        # Native Proxmox VM for development
        # Deploy with: sudo nixos-rebuild switch --flake .#dev-server
        dev-server = nixpkgs.lib.nixosSystem {
          system = "x86_64-linux";
          modules = [
            ./nixos/dev-server.nix
            ./nixos/bloud.nix
          ];
        };

        # Bootable appliance ISO (x86_64 only)
        # Build with: nix build .#packages.x86_64-linux.iso
        iso = nixpkgs.lib.nixosSystem {
          system = "x86_64-linux";
          modules = [
            ./nixos/iso.nix
            # Include the installed system's store closure so nixos-install
            # can copy it to /mnt without needing network access.
            # Also pass the exact store path so the installer uses --system
            # instead of --flake, bypassing flake re-evaluation hash mismatch.
            {
              isoImage.storeContents = [
                self.nixosConfigurations.bloud.config.system.build.toplevel
              ];
              bloud.installer.systemPath = "${self.nixosConfigurations.bloud.config.system.build.toplevel}";
            }
          ];
        };

        # Installed system — applied to disk by the Bloud installer
        # nixos-install --flake <pkg>/share/bloud-installer/bloud#bloud
        bloud = nixpkgs.lib.nixosSystem {
          system = "x86_64-linux";
          modules = [
            ./nixos/installed.nix
            ./nixos/bloud.nix
          ];
        };
      };

      # Packages for building images
      packages = forAllSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        (if system == "x86_64-linux" then {
          # Bootable appliance ISO
          iso = self.nixosConfigurations.iso.config.system.build.isoImage;
        } else {})
      );

      # Development shells for each platform
      devShells = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            buildInputs = with pkgs; [
              go
              nodejs
            ];
          };
        }
      );

      # NixOS integration tests
      # Run with: nix flake check
      # Or: nix build .#checks.<system>.core-services
      checks = forAllSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        {
          core-services = import ./tests/nixos/core-services.nix { inherit pkgs; };
        }
      );
    };
}
