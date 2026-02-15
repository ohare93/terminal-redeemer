{
  description = "terminal-redeemer: terminal session history and restore";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
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
      })
    // {
      homeManagerModules.terminal-redeemer = import ./modules/home-manager/terminal-redeemer.nix;
    };
}
