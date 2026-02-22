{
  description = "Bloud - Home Cloud Operating System";

  inputs = {
    # Use nixos-24.11 for better nixos-generators compatibility
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";
    nixos-generators = {
      url = "github:nix-community/nixos-generators";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, nixos-generators }:
    let
      # Support both Apple Silicon and Intel Macs
      supportedSystems = [ "aarch64-linux" "x86_64-linux" ];

      # Helper to generate outputs for each system
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
    in
    {
      # NixOS configurations
      nixosConfigurations = {
        # Development VM configuration
        vm-dev = nixpkgs.lib.nixosSystem {
          system = "aarch64-linux"; # Apple Silicon default
          modules = [
            ./nixos/vm-dev.nix
            ./nixos/bloud.nix
          ];
        };

        # Intel VM variant
        vm-dev-x86 = nixpkgs.lib.nixosSystem {
          system = "x86_64-linux";
          modules = [
            ./nixos/vm-dev.nix
            ./nixos/bloud.nix
          ];
        };

        # Test VM configuration (runs on different ports from dev)
        vm-test = nixpkgs.lib.nixosSystem {
          system = "aarch64-linux"; # Apple Silicon default
          modules = [
            ./nixos/vm-test.nix
            ./nixos/bloud.nix
          ];
        };

        # Intel test VM variant
        vm-test-x86 = nixpkgs.lib.nixosSystem {
          system = "x86_64-linux";
          modules = [
            ./nixos/vm-test.nix
            ./nixos/bloud.nix
          ];
        };

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
            {
              isoImage.storeContents = [
                self.nixosConfigurations.bloud.config.system.build.toplevel
              ];
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
        {
          # Lima-compatible disk image with NixOS 24.05
          lima-image = nixos-generators.nixosGenerate {
            inherit pkgs;
            format = "raw-efi";
            modules = [ ./nixos/lima-image.nix ];
          };
        }
        // (if system == "x86_64-linux" then {
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
              lima
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
