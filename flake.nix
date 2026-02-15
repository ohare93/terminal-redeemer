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
    flake-utils.lib.eachSystem [ "x86_64-linux" ] (system:
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
          meta.description = "redeem CLI";
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
          let
            hmCfg = home-manager.lib.homeManagerConfiguration {
              inherit pkgs;
              modules = [
                self.homeManagerModules.terminal-redeemer
                {
                  home.username = "test";
                  home.homeDirectory = "/home/test";
                  home.stateVersion = "24.05";
                  programs.terminal-redeemer = {
                    enable = true;
                    package = self.packages.${system}.terminal-redeemer;
                    capture.interval = "30s";
                    capture.snapshotEvery = 7;
                    capture.niriCommand = "niri msg -j windows";
                    retention.days = 14;
                    retention.prune.enable = true;
                    retention.prune.onCalendar = "hourly";
                    processWhitelist = [ "opencode" "claude" "zellij" ];
                    processWhitelistExtra = [ "tmux" ];
                    processIncludeSessionTag = false;
                    restore.appAllowlist = {
                      firefox = "firefox --new-window";
                    };
                    terminal.command = "foot";
                    terminal.zellijAttachOrCreate = false;
                  };
                }
              ];
            };
            cfg = hmCfg.config;
            rendered = cfg.programs.terminal-redeemer.renderedConfig;
            captureExecRaw = cfg.systemd.user.services.terminal-redeemer-capture.Service.ExecStart;
            pruneExecRaw = cfg.systemd.user.services.terminal-redeemer-prune.Service.ExecStart;
            captureExec = if builtins.isList captureExecRaw then builtins.concatStringsSep " " captureExecRaw else captureExecRaw;
            pruneExec = if builtins.isList pruneExecRaw then builtins.concatStringsSep " " pruneExecRaw else pruneExecRaw;
          in
          assert rendered.capture.snapshotEvery == 7;
          assert rendered.capture.interval == "30s";
          assert rendered.capture.niriCommand == "niri msg -j windows";
          assert rendered.retention.days == 14;
          assert rendered.processMetadata.whitelist == [ "opencode" "claude" "zellij" ];
          assert rendered.processMetadata.whitelistExtra == [ "tmux" ];
          assert rendered.processMetadata.includeSessionTag == false;
          assert rendered.restore.terminal.command == "foot";
          assert rendered.restore.terminal.zellijAttachOrCreate == false;
          assert rendered.restore.appAllowlist.firefox == "firefox --new-window";
          assert builtins.match ".* --config .*/terminal-redeemer/config.yaml .*" captureExec != null;
          assert builtins.match ".* capture once" captureExec != null;
          assert builtins.match ".* --config .*/terminal-redeemer/config.yaml .*" pruneExec != null;
          assert builtins.match ".* prune run" pruneExec != null;
          assert builtins.match ".*--state-dir.*" captureExec == null;
          assert builtins.match ".*--days.*" pruneExec == null;
          assert cfg.systemd.user.timers.terminal-redeemer-prune.Timer.OnCalendar == "hourly";
          hmCfg.activationPackage;

        checks.hm-module-prune-default-disabled =
          let
            hmCfg = home-manager.lib.homeManagerConfiguration {
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
            };
            cfg = hmCfg.config;
          in
          assert !(cfg.systemd.user.services ? terminal-redeemer-prune);
          assert !(cfg.systemd.user.timers ? terminal-redeemer-prune);
          hmCfg.activationPackage;
      })
    // {
      homeManagerModules.terminal-redeemer = import ./modules/home-manager/terminal-redeemer.nix;
    };
}
