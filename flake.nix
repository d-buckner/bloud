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
