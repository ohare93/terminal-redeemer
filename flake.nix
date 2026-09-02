{
  description = "terminal-redeemer: terminal placement resume and remote sessions";

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
          vendorHash = "sha256-sDiLEcBE6qw6bM0+AlyAN47W1f7wySG8vYcCTrbIQSU=";
          subPackages = [ "cmd/redeem" ];

          meta = with pkgs.lib; {
            description = "CLI for terminal placement resume and remote sessions";
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

        checks.packaged-cli = pkgs.runCommand "terminal-redeemer-packaged-cli" { } ''
          ${self.packages.${system}.terminal-redeemer}/bin/redeem --help > root-help
          grep -q "Refresh this boot's rolling terminal checkpoint" root-help
          grep -q "Restore prior-boot placement or reconcile all recovery sessions" root-help
          grep -q "Create, pick, pin, apply, or temporarily follow remote terminals" root-help
          grep -q "Read-only capture/resume/mirror diagnostics" root-help

          ${self.packages.${system}.terminal-redeemer}/bin/redeem mirror --help > mirror-help
          grep -q "Manually pick and attach visible or headless live sessions" mirror-help
          grep -q "Replace one pinned projection set from fresh exact evidence" mirror-help
          grep -q "Temporarily follow one selected source workspace in the foreground" mirror-help
          ${self.packages.${system}.terminal-redeemer}/bin/redeem mirror new --help > /dev/null 2> mirror-new-help
          grep -q -- "-source-workspace" mirror-new-help
          ${self.packages.${system}.terminal-redeemer}/bin/redeem mirror open --help > /dev/null 2> mirror-open-help
          ${self.packages.${system}.terminal-redeemer}/bin/redeem mirror save --help > /dev/null 2> mirror-save-help
          ${self.packages.${system}.terminal-redeemer}/bin/redeem mirror apply --help > /dev/null 2> mirror-apply-help
          ${self.packages.${system}.terminal-redeemer}/bin/redeem mirror follow --help > /dev/null 2> mirror-follow-help
          grep -q -- "-max-per-poll" mirror-follow-help
          grep -q -- "-max-total" mirror-follow-help
          grep -q -- "minimum 2s" mirror-follow-help
          ${self.packages.${system}.terminal-redeemer}/bin/redeem resume --help > /dev/null 2> resume-help
          grep -q -- "same boot: reconcile all exact ACTIVE sessions" resume-help
          grep -q -- "maximum age for prior dead-session resurrection" resume-help
          touch "$out"
        '';

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
                    capture.niriCommand = "niri msg -j windows";
                    retention.days = 14;
                    retention.prune.enable = true;
                    retention.prune.onCalendar = "hourly";
                    processWhitelist = [ "opencode" "claude" "zellij" ];
                    processWhitelistExtra = [ "tmux" ];
                    processIncludeSessionTag = false;
                    resume.onStartup = true;
                    resume.maxCheckpointAge = "12h";
                    resume.unresolvedWorkspace = "current";
                    resume.timeout = "8s";
                    resume.pollInterval = "25ms";
                    resume.terminalCommand = "foot";
                    mirror = {
                      sourceHost = "source.example";
                      sourceWorkspace = "agentleman";
                      sshCommand = "custom-ssh";
                      sshOptions = [ "-p" "2222" ];
                      snapshotCommand = [ "remote-redeem" "mirror" "snapshot" ];
                      launcherCommand = "custom-kitty";
                      selfCommand = "/run/current-system/sw/bin/redeem";
                      appID = "redeem-owned";
                      openDelay = "25ms";
                      niriCommand = "custom-niri";
                      clipboard = {
                        enabled = true;
                        command = "custom-wl-paste";
                        scpCommand = "custom-scp";
                        scpOptions = [ "-P" "2222" ];
                        kittyCommand = "custom-kitty";
                        tempDir = "/var/tmp";
                        mimeTypes = [ "image/webp" ];
                      };
                    };
                  };
                }
              ];
            };
            cfg = hmCfg.config;
            rendered = cfg.programs.terminal-redeemer.renderedConfig;
            captureService = cfg.systemd.user.services.terminal-redeemer-capture;
            captureTimer = cfg.systemd.user.timers.terminal-redeemer-capture;
            resumeService = cfg.systemd.user.services.terminal-redeemer-resume;
            captureExecRaw = captureService.Service.ExecStart;
            resumeExecRaw = resumeService.Service.ExecStart;
            pruneExecRaw = cfg.systemd.user.services.terminal-redeemer-prune.Service.ExecStart;
            captureExec = if builtins.isList captureExecRaw then builtins.concatStringsSep " " captureExecRaw else captureExecRaw;
            resumeExec = if builtins.isList resumeExecRaw then builtins.concatStringsSep " " resumeExecRaw else resumeExecRaw;
            pruneExec = if builtins.isList pruneExecRaw then builtins.concatStringsSep " " pruneExecRaw else pruneExecRaw;
          in
          assert rendered.capture.interval == "30s";
          assert rendered.capture.niriCommand == "niri msg -j windows";
          assert rendered.retention.days == 14;
          assert rendered.processMetadata.whitelist == [ "opencode" "claude" "zellij" ];
          assert rendered.processMetadata.whitelistExtra == [ "tmux" ];
          assert rendered.processMetadata.includeSessionTag == false;
          assert rendered.resume.onStartup;
          assert pkgs.lib.hasInfix ''spawn-at-startup "'' cfg.programs.terminal-redeemer.resume.niriIntegrationFragment;
          assert pkgs.lib.hasInfix ''"--user" "restart" "terminal-redeemer-resume.service";'' cfg.programs.terminal-redeemer.resume.niriIntegrationFragment;
          assert rendered.resume.terminalCommand == "foot";
          assert rendered.resume.maxCheckpointAge == "12h";
          assert rendered.resume.unresolvedWorkspace == "current";
          assert rendered.resume.timeout == "8s";
          assert rendered.resume.pollInterval == "25ms";
          assert cfg.programs.terminal-redeemer.mirror.localCommand == [ "custom-kitty" ];
          assert cfg.programs.terminal-redeemer.mirror.newCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "new" "--host" "source.example" "--source-workspace" "agentleman" ];
          assert cfg.programs.terminal-redeemer.mirror.openCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "open" "--host" "source.example" ];
          assert cfg.programs.terminal-redeemer.mirror.saveCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "save" "--host" "source.example" ];
          assert cfg.programs.terminal-redeemer.mirror.applyCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "apply" "--host" "source.example" ];
          assert cfg.programs.terminal-redeemer.mirror.followCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "follow" "--host" "source.example" ];
          assert pkgs.lib.hasInfix ''Mod+Return { spawn "custom-kitty"; }'' cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert pkgs.lib.hasInfix ''Mod+Shift+Return { spawn '' cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert pkgs.lib.hasInfix ''"mirror" "new" "--host" "source.example" "--source-workspace" "agentleman"; }'' cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert pkgs.lib.hasInfix ''Mod+Ctrl+Return { spawn '' cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert pkgs.lib.hasInfix ''"mirror" "open" "--host" "source.example"; }'' cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert rendered.mirror.sourceHost == "source.example";
          assert !(rendered.mirror ? sourceWorkspace);
          assert !(pkgs.lib.hasInfix ''"mirror" "follow"'' cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment);
          assert rendered.mirror.sshOptions == [ "-p" "2222" ];
          assert rendered.mirror.snapshotCommand == [ "remote-redeem" "mirror" "snapshot" ];
          assert rendered.mirror.appID == "redeem-owned";
          assert rendered.mirror.openDelay == "25ms";
          assert rendered.mirror.clipboard.scpOptions == [ "-P" "2222" ];
          assert rendered.mirror.clipboard.mimeTypes == [ "image/webp" ];
          assert builtins.match ".* --config .*/terminal-redeemer/config.yaml .*" captureExec != null;
          assert builtins.match ".* capture once" captureExec != null;
          assert builtins.match ".* --config .*/terminal-redeemer/config.yaml .*" pruneExec != null;
          assert builtins.match ".* prune run" pruneExec != null;
          assert builtins.match ".* --config .*/terminal-redeemer/config.yaml resume --all" resumeExec != null;
          assert builtins.match ".*resume.*resume.*" resumeExec == null;
          assert resumeService.Service.Type == "oneshot";
          assert resumeService.Service.Restart == "on-failure";
          assert resumeService.Unit.StartLimitBurst == 5;
          assert builtins.elem "graphical-session.target" resumeService.Unit.After;
          assert builtins.elem "graphical-session.target" resumeService.Unit.PartOf;
          assert resumeService.Install.WantedBy == [ "graphical-session.target" ];
          assert builtins.elem "terminal-redeemer-resume.service" captureService.Unit.After;
          assert builtins.match ".*--state-dir.*" captureExec == null;
          assert builtins.match ".*--days.*" pruneExec == null;
          assert captureService.Service.Type == "oneshot";
          assert builtins.elem "graphical-session.target" captureService.Unit.After;
          assert builtins.elem "graphical-session.target" captureService.Unit.PartOf;
          assert captureTimer.Timer.OnActiveSec == "30s";
          assert captureTimer.Timer.OnUnitActiveSec == "30s";
          assert !(captureTimer.Timer ? Persistent);
          assert builtins.elem "graphical-session.target" captureTimer.Unit.After;
          assert builtins.elem "terminal-redeemer-resume.service" captureTimer.Unit.After;
          assert builtins.elem "graphical-session.target" captureTimer.Unit.PartOf;
          assert captureTimer.Install.WantedBy == [ "graphical-session.target" ];
          assert cfg.systemd.user.timers.terminal-redeemer-prune.Timer.OnCalendar == "hourly";
          assert cfg.systemd.user.timers.terminal-redeemer-prune.Timer.Persistent;
          pkgs.runCommand "terminal-redeemer-hm-module-eval" { } ''
            test -d ${hmCfg.activationPackage}
            touch "$out"
          '';


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
            rendered = cfg.programs.terminal-redeemer.renderedConfig;
            captureTimer = cfg.systemd.user.timers.terminal-redeemer-capture;
          in
          assert rendered.capture.interval == "60s";
          assert !rendered.resume.onStartup;
          assert cfg.programs.terminal-redeemer.resume.niriIntegrationFragment == "";
          assert rendered.resume.maxCheckpointAge == "24h";
          assert rendered.resume.unresolvedWorkspace == "current";
          assert captureTimer.Timer.OnActiveSec == "60s";
          assert captureTimer.Timer.OnUnitActiveSec == "60s";
          assert !(captureTimer.Timer ? Persistent);
          assert !(builtins.elem "terminal-redeemer-resume.service" captureTimer.Unit.After);
          assert !(cfg.systemd.user.services ? terminal-redeemer-prune);
          assert !(cfg.systemd.user.timers ? terminal-redeemer-prune);
          assert !(cfg.systemd.user.services ? terminal-redeemer-resume);
          assert !(cfg.systemd.user.services ? terminal-redeemer-follow);
          assert !(cfg.systemd.user.timers ? terminal-redeemer-follow);
          assert cfg.programs.terminal-redeemer.mirror.newCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "new" ];
          assert cfg.programs.terminal-redeemer.mirror.openCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "open" ];
          assert cfg.programs.terminal-redeemer.mirror.saveCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "save" ];
          assert cfg.programs.terminal-redeemer.mirror.applyCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "apply" ];
          assert cfg.programs.terminal-redeemer.mirror.followCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "mirror" "follow" ];
          assert pkgs.lib.hasInfix ''Mod+Return { spawn "kitty"; }'' cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert pkgs.lib.hasInfix "Mod+Shift+Return" cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert pkgs.lib.hasInfix "Mod+Ctrl+Return" cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert !(pkgs.lib.hasInfix ''"mirror" "follow"'' cfg.programs.terminal-redeemer.mirror.niriIntegrationFragment);
          hmCfg.activationPackage;

        checks.nixos-module-eval =
          let
            nixosCfg = nixpkgs.lib.nixosSystem {
              inherit system;
              modules = [
                home-manager.nixosModules.home-manager
                self.nixosModules.terminal-redeemer
                {
                  system.stateVersion = "24.05";
                  users.users.test = {
                    isNormalUser = true;
                    home = "/home/test";
                  };
                  programs.terminal-redeemer = {
                    enable = true;
                    users.test = {
                      package = self.packages.${system}.terminal-redeemer;
                      resume.onStartup = true;
                      resume.terminalCommand = "foot";
                      mirror.sourceHost = "source.example";
                      mirror.sourceWorkspace = "agentleman";
                    };
                  };
                }
              ];
            };
            hmUser = nixosCfg.config.home-manager.users.test;
            rendered = hmUser.programs.terminal-redeemer.renderedConfig;
          in
          assert rendered.resume.onStartup;
          assert pkgs.lib.hasInfix ''"restart" "terminal-redeemer-resume.service"'' hmUser.programs.terminal-redeemer.resume.niriIntegrationFragment;
          assert rendered.resume.terminalCommand == "foot";
          assert rendered.mirror.sourceHost == "source.example";
          assert pkgs.lib.hasInfix ''Mod+Return { spawn "kitty"; }'' hmUser.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert pkgs.lib.hasInfix ''"mirror" "new" "--host" "source.example" "--source-workspace" "agentleman"; }'' hmUser.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert pkgs.lib.hasInfix ''"mirror" "open" "--host" "source.example"; }'' hmUser.programs.terminal-redeemer.mirror.niriIntegrationFragment;
          assert hmUser.systemd.user.services ? terminal-redeemer-resume;
          pkgs.runCommand "nixos-module-eval" { } ''
            touch "$out"
          '';
      })
    // {
      homeManagerModules.terminal-redeemer = { pkgs, ... }: {
        imports = [ ./modules/home-manager/terminal-redeemer.nix ];
      };
      nixosModules.terminal-redeemer = { ... }: {
        _module.args.terminalRedeemerHomeManagerModule = self.homeManagerModules.terminal-redeemer;
        imports = [ ./modules/nixos/terminal-redeemer.nix ];
      };
    };
}
