{
  description = "terminal-redeemer: terminal session history and restore";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    home-manager = {
      url = "github:nix-community/home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, flake-utils, home-manager }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in {
        packages.terminal-redeemer = pkgs.buildGoModule {
          pname = "terminal-redeemer";
          version = "0.1.0";
          src = ./.;
          vendorHash = null;
          subPackages = [ "cmd/redeem" ];

          meta = with pkgs.lib; {
            description = "CLI for rewindable terminal session restore";
            license = licenses.mit;
            platforms = platforms.linux;
            mainProgram = "redeem";
          };
        };

        packages.default = self.packages.${system}.terminal-redeemer;

        apps.redeem = {
          type = "app";
          program = "${self.packages.${system}.terminal-redeemer}/bin/redeem";
        };

        apps.default = self.apps.${system}.redeem;

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            gotools
            jq
          ];
        };

        checks.hm-module-eval =
          (home-manager.lib.homeManagerConfiguration {
            inherit pkgs;
            modules = [
              self.homeManagerModules.terminal-redeemer
              {
                home.username = "test";
                home.homeDirectory = "/home/test";
                home.stateVersion = "24.05";
                programs.terminal-redeemer.enable = true;
                programs.terminal-redeemer.package = self.packages.${system}.terminal-redeemer;
              }
            ];
          }).activationPackage;
      })
    // {
      homeManagerModules.terminal-redeemer = import ./modules/home-manager/terminal-redeemer.nix;
    };
}
