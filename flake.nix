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
        packages.niri = pkgs.niri;
        packages.zellij = pkgs.zellij;

        packages.terminal-redeemer = pkgs.buildGoModule {
          pname = "terminal-redeemer";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-4tboLhjTNXIeaDudavqjsQ3iOPehgs929OR9PKPX+0c=";
          subPackages = [ "cmd/redeem" ];
          ldflags = [
            "-X github.com/jmo/terminal-redeemer/internal/config.DefaultSelfExecutable=${builtins.placeholder "out"}/bin/redeem"
            "-X github.com/jmo/terminal-redeemer/internal/config.DefaultKittyExecutable=${pkgs.lib.getExe pkgs.kitty}"
            "-X github.com/jmo/terminal-redeemer/internal/config.DefaultTransportExecutable=${pkgs.lib.getExe pkgs.openssh}"
            "-X github.com/jmo/terminal-redeemer/internal/config.DefaultZellijExecutable=${pkgs.lib.getExe pkgs.zellij}"
            "-X github.com/jmo/terminal-redeemer/internal/config.DefaultNiriExecutable=${pkgs.lib.getExe pkgs.niri}"
            "-X github.com/jmo/terminal-redeemer/internal/config.DefaultSystemctlExecutable=${pkgs.lib.getExe' pkgs.systemd "systemctl"}"
          ];

          meta = with pkgs.lib; {
            description = "CLI for rewindable terminal session restore";
            license = licenses.mit;
            platforms = platforms.linux;
            mainProgram = "redeem";
          };
        };

        packages.host-leech-consumer-contract = pkgs.runCommand "terminal-redeemer-host-leech-consumer-contract-1.2.0" { } ''
          mkdir -p "$out/share/terminal-redeemer/host-leech-slices/v1"
          cp ${./contracts/host-leech-slices/v1/consumer-contract.json} "$out/share/terminal-redeemer/host-leech-slices/v1/consumer-contract.json"
          cp ${./contracts/host-leech-slices/v1/consumer-contract.schema.json} "$out/share/terminal-redeemer/host-leech-slices/v1/consumer-contract.schema.json"
          cp ${./contracts/host-leech-slices/v1/niri-bindings.kdl.in} "$out/share/terminal-redeemer/host-leech-slices/v1/niri-bindings.kdl.in"
          substitute ${./contracts/host-leech-slices/v1/niri-bindings.kdl.in} "$out/share/terminal-redeemer/host-leech-slices/v1/niri-bindings.kdl" \
            --replace-fail '@REDEEM@' '${pkgs.lib.getExe self.packages.${system}.terminal-redeemer}'
        '';

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

        devShells.niri-spike = pkgs.mkShell {
          packages = [
            pkgs.niri
            pkgs.kitty
            pkgs.python3
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
                    restore.onStartup = true;
                    restore.appAllowlist = {
                      firefox = "firefox --new-window";
                    };
                    restore.appMode = {
                      firefox = "oneshot";
                    };
                    restore.reconcileWorkspaceMoves = false;
                    restore.workspaceReconcileDelay = "3s";
                    restore.maxCheckpointAge = "12h";
                    restore.unresolvedWorkspace = "current";
                    terminal.command = "foot";
                    terminal.zellijAttachOrCreate = false;
                    slice.leechMode.enable = true;
                    slice.controller = {
                      enable = true;
                      hostID = "lattice";
                      leechID = "overton";
                      pollInterval = "3s";
                      controlTimeout = "4s";
                      retryWindow = "45s";
                      sourceGoneGrace = "6s";
                      sourceGoneConfirmations = 3;
                    };
                    mirror = {
                      sourceHost = "source.example";
                      sshCommand = "custom-ssh";
                      sshOptions = [ "-p" "2222" ];
                      snapshotCommand = [ "remote-redeem" "mirror" "snapshot" ];
                      launcherCommand = "custom-kitty";
                      selfCommand = "/run/current-system/sw/bin/redeem";
                      appID = "redeem-owned";
                      defaultMode = "watch";
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
            controllerService = cfg.systemd.user.services.terminal-redeemer-slice-controller;
            captureExecRaw = captureService.Service.ExecStart;
            resumeExecRaw = resumeService.Service.ExecStart;
            pruneExecRaw = cfg.systemd.user.services.terminal-redeemer-prune.Service.ExecStart;
            controllerExecRaw = controllerService.Service.ExecStart;
            captureExec = if builtins.isList captureExecRaw then builtins.concatStringsSep " " captureExecRaw else captureExecRaw;
            resumeExec = if builtins.isList resumeExecRaw then builtins.concatStringsSep " " resumeExecRaw else resumeExecRaw;
            pruneExec = if builtins.isList pruneExecRaw then builtins.concatStringsSep " " pruneExecRaw else pruneExecRaw;
            controllerExec = if builtins.isList controllerExecRaw then builtins.concatStringsSep " " controllerExecRaw else controllerExecRaw;
          in
          assert rendered.capture.snapshotEvery == 7;
          assert rendered.capture.interval == "30s";
          assert rendered.capture.niriCommand == "niri msg -j windows";
          assert rendered.retention.days == 14;
          assert rendered.processMetadata.whitelist == [ "opencode" "claude" "zellij" ];
          assert rendered.processMetadata.whitelistExtra == [ "tmux" ];
          assert rendered.processMetadata.includeSessionTag == false;
          assert rendered.restore.onStartup;
          assert rendered.restore.terminal.command == "foot";
          assert rendered.restore.terminal.zellijAttachOrCreate == false;
          assert rendered.restore.appAllowlist.firefox == "firefox --new-window";
          assert rendered.restore.appMode.firefox == "oneshot";
          assert rendered.restore.reconcileWorkspaceMoves == false;
          assert rendered.restore.workspaceReconcileDelay == "3s";
          assert rendered.restore.maxCheckpointAge == "12h";
          assert rendered.restore.unresolvedWorkspace == "current";
          assert rendered.slice.leechModeEnabled;
          assert cfg.programs.terminal-redeemer.slice.launchCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "slice" "launch" ];
          assert cfg.programs.terminal-redeemer.slice.closeFocusedCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "slice" "close-focused" ];
          assert cfg.programs.terminal-redeemer.slice.manageCommand == [
            (pkgs.lib.getExe pkgs.kitty)
            "--config" "NONE"
            "--class" "terminal-redeemer-slice-manager"
            "--override" "confirm_os_window_close=0"
            "--title" "Terminal Redeemer Slices"
            "-e" (pkgs.lib.getExe self.packages.${system}.terminal-redeemer)
            "--config" "/home/test/.config/terminal-redeemer/config.yaml"
            "slice" "manage"
          ];
          assert builtins.match ".*Mod\\+Return.*slice.*launch.*Mod\\+W.*slice.*close-focused.*" cfg.programs.terminal-redeemer.slice.niriIntegrationFragment != null;
          assert rendered.slice.selfCommand == pkgs.lib.getExe self.packages.${system}.terminal-redeemer;
          assert rendered.slice.kittyCommand == pkgs.lib.getExe pkgs.kitty;
          assert rendered.slice.transportCommand == pkgs.lib.getExe pkgs.openssh;
          assert rendered.slice.expectedNiriVersion == "26.04";
          assert rendered.slice.niriCommand == pkgs.lib.getExe self.packages.${system}.niri;
          assert rendered.slice.zellijCommand == pkgs.lib.getExe self.packages.${system}.zellij;
          assert rendered.slice.systemctlCommand == pkgs.lib.getExe' pkgs.systemd "systemctl";
          assert rendered.slice.rpcCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "slice" "rpc" ];
          assert rendered.slice.clipboard.enabled == false;
          assert rendered.slice.graphicalContextKeys == [ "NIRI_SOCKET" "WAYLAND_DISPLAY" "XDG_RUNTIME_DIR" ];
          assert rendered.slice.controller.enabled;
          assert rendered.slice.controller.hostID == "lattice";
          assert rendered.slice.controller.leechID == "overton";
          assert rendered.slice.controller.retryWindow == "45s";
          assert rendered.slice.controller.sourceGoneConfirmations == 3;
          assert rendered.slice.controller.authorityMode == "host_location";
          assert rendered.slice.controller.leechWriteAuthorized == false;
          assert controllerService.Service.Type == "simple";
          assert controllerService.Service.Restart == "on-failure";
          assert builtins.match ".*slice controller run" controllerExec != null;
          assert controllerService.Install.WantedBy == [ "graphical-session.target" ];
          assert rendered.mirror.sourceHost == "source.example";
          assert rendered.mirror.sshOptions == [ "-p" "2222" ];
          assert rendered.mirror.snapshotCommand == [ "remote-redeem" "mirror" "snapshot" ];
          assert rendered.mirror.appID == "redeem-owned";
          assert rendered.mirror.defaultMode == "watch";
          assert rendered.mirror.openDelay == "25ms";
          assert rendered.mirror.clipboard.scpOptions == [ "-P" "2222" ];
          assert rendered.mirror.clipboard.mimeTypes == [ "image/webp" ];
          assert builtins.match ".* --config .*/terminal-redeemer/config.yaml .*" captureExec != null;
          assert builtins.match ".* capture once" captureExec != null;
          assert builtins.match ".* --config .*/terminal-redeemer/config.yaml .*" pruneExec != null;
          assert builtins.match ".* prune run" pruneExec != null;
          assert builtins.match ".* --config .*/terminal-redeemer/config.yaml resume" resumeExec != null;
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
          assert builtins.elem "graphical-session.target" captureTimer.Unit.PartOf;
          assert captureTimer.Install.WantedBy == [ "graphical-session.target" ];
          assert cfg.systemd.user.timers.terminal-redeemer-prune.Timer.OnCalendar == "hourly";
          assert cfg.systemd.user.timers.terminal-redeemer-prune.Timer.Persistent;
          pkgs.runCommand "terminal-redeemer-hm-module-eval" { } ''
            test -d ${hmCfg.activationPackage}
            test "$(${rendered.slice.niriCommand} --version)" = "niri ${rendered.slice.expectedNiriVersion} (Nixpkgs)"
            test "$(${rendered.slice.zellijCommand} --version)" = "zellij 0.44.3"
            touch "$out"
          '';

        checks.hm-module-rejects-leech-location =
          let
            attempted = builtins.tryEval ((home-manager.lib.homeManagerConfiguration {
              inherit pkgs;
              modules = [
                self.homeManagerModules.terminal-redeemer
                {
                  home.username = "test";
                  home.homeDirectory = "/home/test";
                  home.stateVersion = "24.05";
                  programs.terminal-redeemer.slice.controller.authorityMode = "leech_location";
                }
              ];
            }).activationPackage.drvPath);
          in
          assert !attempted.success;
          pkgs.runCommand "hm-module-rejects-leech-location" { } "touch $out";

        checks.hm-module-rejects-leech-write-authorized =
          let
            attempted = builtins.tryEval ((home-manager.lib.homeManagerConfiguration {
              inherit pkgs;
              modules = [
                self.homeManagerModules.terminal-redeemer
                {
                  home.username = "test";
                  home.homeDirectory = "/home/test";
                  home.stateVersion = "24.05";
                  programs.terminal-redeemer.slice.controller.leechWriteAuthorized = true;
                }
              ];
            }).activationPackage.drvPath);
          in
          assert !attempted.success;
          pkgs.runCommand "hm-module-rejects-leech-write-authorized" { } "touch $out";

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
          assert !rendered.restore.onStartup;
          assert rendered.restore.maxCheckpointAge == "24h";
          assert rendered.restore.unresolvedWorkspace == "current";
          assert captureTimer.Timer.OnActiveSec == "60s";
          assert captureTimer.Timer.OnUnitActiveSec == "60s";
          assert !(captureTimer.Timer ? Persistent);
          assert !(cfg.systemd.user.services ? terminal-redeemer-prune);
          assert !(cfg.systemd.user.timers ? terminal-redeemer-prune);
          assert !(cfg.systemd.user.services ? terminal-redeemer-resume);
          assert !(cfg.systemd.user.services ? terminal-redeemer-slice-controller);
          assert rendered.slice.controller.enabled == false;
          assert rendered.slice.leechModeEnabled == false;
          assert cfg.programs.terminal-redeemer.slice.launchCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "slice" "launch" ];
          assert cfg.programs.terminal-redeemer.slice.closeFocusedCommand == [ (pkgs.lib.getExe self.packages.${system}.terminal-redeemer) "slice" "close-focused" ];
          assert cfg.programs.terminal-redeemer.slice.manageCommand == [
            (pkgs.lib.getExe pkgs.kitty)
            "--config" "NONE"
            "--class" "terminal-redeemer-slice-manager"
            "--override" "confirm_os_window_close=0"
            "--title" "Terminal Redeemer Slices"
            "-e" (pkgs.lib.getExe self.packages.${system}.terminal-redeemer)
            "--config" "/home/test/.config/terminal-redeemer/config.yaml"
            "slice" "manage"
          ];
          assert builtins.match ".*Mod\\+Return.*slice.*launch.*Mod\\+W.*slice.*close-focused.*" cfg.programs.terminal-redeemer.slice.niriIntegrationFragment != null;
          hmCfg.activationPackage;

        checks.zellij-live-only-attachment-spike =
          pkgs.runCommand "terminal-redeemer-zellij-live-only-attachment-spike" {
            nativeBuildInputs = [
              pkgs.bash
              pkgs.coreutils
              pkgs.gnugrep
              pkgs.util-linux
              pkgs.zellij
            ];
          } ''
            export ZELLIJ_BIN=${pkgs.lib.getExe pkgs.zellij}
            export SCRIPT_BIN=${pkgs.lib.getExe' pkgs.util-linux "script"}
            export TIMEOUT_BIN=${pkgs.lib.getExe' pkgs.coreutils "timeout"}
            export EXPECTED_ZELLIJ_VERSION=0.44.3
            ${pkgs.bash}/bin/bash ${./scripts/spikes/zellij-live-only-attachment.sh}
            touch "$out"
          '';

        checks.host-leech-consumer-contract =
          assert self.lib.sliceConsumerContract.inventorySchemaVersion == 1;
          assert self.lib.sliceConsumerContract.rpcSchemaVersion == 1;
          assert self.lib.sliceConsumerContract.controllerSchemaVersion == 2;
          assert self.lib.sliceConsumerContract.contractVersion == "1.2.0";
          assert self.lib.sliceConsumerContract.niriVersion == "26.04";
          assert self.lib.sliceConsumerContract.zellijVersion == "0.44.3";
          assert self.lib.sliceConsumerContract.allEligibleIncludesUnnamed;
          assert !self.lib.sliceConsumerContract.allEligibleRoutesLaunches;
          assert self.lib.sliceConsumerContract.authorityMode == "host_location";
          assert !self.lib.sliceConsumerContract.leechWriteAuthorized;
          assert !self.lib.sliceConsumerContract.leechModeEnabledByDefault;
          assert !self.lib.sliceConsumerContract.controllerEnabledByDefault;
          assert !self.lib.sliceConsumerContract.sliceClipboardEnabledByDefault;
          assert !self.lib.sliceConsumerContract.bindingsInstalledAutomatically;
          assert self.lib.sliceConsumerContract.legacyAttachRetained;
          assert !self.lib.sliceConsumerContract.watchSupported;
          assert !self.lib.sliceConsumerContract.automaticLocalFallbackAfterRemoteIntent;
          assert self ? homeManagerModules && self.homeManagerModules ? terminal-redeemer;
          assert self ? nixosModules && self.nixosModules ? terminal-redeemer;
          assert self.packages.${system} ? terminal-redeemer;
          assert self.packages.${system} ? host-leech-consumer-contract;
          pkgs.runCommand "terminal-redeemer-host-leech-consumer-contract-check" {
          nativeBuildInputs = [ pkgs.coreutils pkgs.gnugrep pkgs.jq pkgs.check-jsonschema ];
        } ''
          cd ${./.}
          contractOut=${self.packages.${system}.host-leech-consumer-contract}/share/terminal-redeemer/host-leech-slices/v1
          schema=contracts/host-leech-slices/v1/consumer-contract.schema.json
          contract=contracts/host-leech-slices/v1/consumer-contract.json
          check-jsonschema --schemafile "$schema" "$contract"
          reject_contract_mutation() {
            name=$1
            filter=$2
            mutated="$TMPDIR/consumer-contract-$name.json"
            jq "$filter" "$contract" > "$mutated"
            if check-jsonschema --schemafile "$schema" "$mutated" >/dev/null 2>&1; then
              echo "consumer contract schema accepted semantic drift: $name" >&2
              exit 1
            fi
          }
          reject_contract_mutation drops '.drops.survives_source_replacement = false'
          reject_contract_mutation command-argv '.commands.launch_reconnect[3] = "--fallback"'
          reject_contract_mutation manage-command '.commands.manage[2] = "status"'
          reject_contract_mutation controller-operations '.commands.controller_operations -= ["all-enable"]'
          reject_contract_mutation selection-formula '.selection.formula = "selected_workspace"'
          reject_contract_mutation selection-downgrade '.selection.downgrade_requires_disable_first = false'
          reject_contract_mutation unnamed-spatial '.selection.unnamed_spatial_policy = "host_location"'
          reject_contract_mutation manage-helper '.integration.manage_helper_option = "programs.terminal-redeemer.slice.launchCommand"'
          reject_contract_mutation authority '.authority.converged_properties -= ["proportional_height"]'
          reject_contract_mutation revisions '.revisions.non_authoritative_observations -= ["degraded"]'
          reject_contract_mutation limitations '.limitations.pinned_version_coupling = false'
          reject_contract_mutation no-fallback '.rollout.automatic_local_fallback_after_remote_intent = true'
          cmp "$contract" "$contractOut/consumer-contract.json"
          cmp contracts/host-leech-slices/v1/consumer-contract.schema.json "$contractOut/consumer-contract.schema.json"
          cmp contracts/host-leech-slices/v1/niri-bindings.kdl.in "$contractOut/niri-bindings.kdl.in"
          test -f "$contractOut/niri-bindings.kdl"
          grep -Fx '    Mod+Return { spawn "@REDEEM@" "slice" "launch"; }' contracts/host-leech-slices/v1/niri-bindings.kdl.in
          grep -Fx '    Mod+W { spawn "@REDEEM@" "slice" "close-focused"; }' contracts/host-leech-slices/v1/niri-bindings.kdl.in
          if grep -Eq '(^|[[:space:]"/])(sh|bash)([[:space:]"/]|$)|[[:space:]]-c([[:space:]]|$)' contracts/host-leech-slices/v1/niri-bindings.kdl.in; then
            echo "Niri template must not introduce a shell" >&2
            exit 1
          fi
          grep -F '${pkgs.lib.getExe self.packages.${system}.terminal-redeemer}' "$contractOut/niri-bindings.kdl"
          redeem=${pkgs.lib.getExe self.packages.${system}.terminal-redeemer}
          "$redeem" slice --help | grep -F 'controller|mode|launch|manage|projection-run|close-focused'
          "$redeem" slice manage --help 2>&1 | grep -F 'refresh-interval'
          "$redeem" slice controller --help | grep -F 'controller <init|run|status|workspace-add|workspace-remove|all-enable|all-disable|pickup|pickup-remove|drop|close|reopen|undo|reconnect|launch-handoff>'
          "$redeem" slice mode --help | grep -F 'mode <enable|disable|status>'
          "$redeem" mirror open --help 2>&1 | grep -F 'attach or watch'
          touch "$out"
        '';

        checks.host-leech-hermetic-matrix = pkgs.buildGoModule {
          pname = "terminal-redeemer-host-leech-hermetic-matrix";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-4tboLhjTNXIeaDudavqjsQ3iOPehgs929OR9PKPX+0c=";
          subPackages = [ "cmd/redeem" ];
          nativeBuildInputs = [
            pkgs.bash
            pkgs.coreutils
            pkgs.gnugrep
            pkgs.niri
            pkgs.util-linux
            pkgs.zellij
          ];
          doCheck = true;
          checkPhase = ''
            runHook preCheck
            export HOME="$TMPDIR/home"
            mkdir -p "$HOME"
            export TERMINAL_REDEEMER_SOAK_ITERATIONS=2000
            export RUN_LOCKED_NIRI_VERSION_CHECK=1
            export NIRI_BIN=${pkgs.lib.getExe pkgs.niri}
            export EXPECTED_NIRI_VERSION='26.04'
            export RUN_LOCKED_ZELLIJ_SPIKE=1
            export ZELLIJ_BIN=${pkgs.lib.getExe pkgs.zellij}
            export SCRIPT_BIN=${pkgs.lib.getExe' pkgs.util-linux "script"}
            export TIMEOUT_BIN=${pkgs.lib.getExe' pkgs.coreutils "timeout"}
            export EXPECTED_ZELLIJ_VERSION=0.44.3
            ${pkgs.bash}/bin/bash scripts/tests/host-leech-hermetic-matrix.sh
            runHook postCheck
          '';
        };

        # Slow process-boundary acceptance remains separate from the focused
        # in-process matrix above. It requires the packaged binary and refuses
        # ambient PATH, graphical state, credentials, SSH agents, or Zellij.
        checks.host-leech-subprocess-acceptance = pkgs.buildGoModule {
          pname = "terminal-redeemer-host-leech-subprocess-acceptance";
          version = "0.1.0";
          src = ./.;
          vendorHash = "sha256-4tboLhjTNXIeaDudavqjsQ3iOPehgs929OR9PKPX+0c=";
          subPackages = [ "cmd/redeem" ];
          nativeBuildInputs = [ pkgs.coreutils pkgs.zellij ];
          doCheck = true;
          checkPhase = ''
            runHook preCheck
            unset SSH_AUTH_SOCK SSH_AGENT_PID NIRI_SOCKET WAYLAND_DISPLAY
            unset ZELLIJ ZELLIJ_SESSION_NAME XDG_CONFIG_HOME XDG_STATE_HOME
            unset XDG_RUNTIME_DIR XDG_CACHE_HOME
            unset TERMINAL_REDEEMER_RETAIN_CRASH_MATRIX TERMINAL_REDEEMER_CRASH_MATRIX_STAGE
            export HOME="$TMPDIR/acceptance-home"
            mkdir -m 700 -p "$HOME"
            export REDEEM_BIN=${pkgs.lib.getExe self.packages.${system}.terminal-redeemer}
            export ZELLIJ_BIN=${pkgs.lib.getExe pkgs.zellij}
            export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
            # A cold `nix flake check` runs this process/crash matrix beside the
            # soak, generated fuzz campaigns, and pinned spikes. Keep it bounded,
            # but allow that intentionally concurrent scheduler load rather than
            # turning an otherwise healthy exact crash gate into a timeout flake.
            ${pkgs.coreutils}/bin/timeout 600s go test -count=1 -v -timeout=570s ./internal/subprocessacceptance
            runHook postCheck
          '';
        };

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
                      restore.onStartup = true;
                      restore.appMode.firefox = "oneshot";
                      restore.reconcileWorkspaceMoves = false;
                      restore.workspaceReconcileDelay = "3s";
                      mirror.sourceHost = "source.example";
                      mirror.defaultMode = "watch";
                    };
                  };
                }
              ];
            };
            hmUser = nixosCfg.config.home-manager.users.test;
            rendered = hmUser.programs.terminal-redeemer.renderedConfig;
          in
          assert rendered.restore.onStartup;
          assert rendered.restore.appMode.firefox == "oneshot";
          assert rendered.restore.reconcileWorkspaceMoves == false;
          assert rendered.restore.workspaceReconcileDelay == "3s";
          assert rendered.mirror.sourceHost == "source.example";
          assert rendered.mirror.defaultMode == "watch";
          assert rendered.slice.niriCommand == pkgs.lib.getExe self.packages.${system}.niri;
          assert rendered.slice.zellijCommand == pkgs.lib.getExe self.packages.${system}.zellij;
          assert hmUser.systemd.user.services ? terminal-redeemer-resume;
          pkgs.runCommand "nixos-module-eval" { } ''
            test "$(${rendered.slice.niriCommand} --version)" = "niri ${rendered.slice.expectedNiriVersion} (Nixpkgs)"
            test "$(${rendered.slice.zellijCommand} --version)" = "zellij 0.44.3"
            touch "$out"
          '';
      })
    // {
      homeManagerModules.terminal-redeemer = { pkgs, ... }: {
        _module.args.terminalRedeemerNiri = self.packages.${pkgs.stdenv.hostPlatform.system}.niri;
        _module.args.terminalRedeemerZellij = self.packages.${pkgs.stdenv.hostPlatform.system}.zellij;
        imports = [ ./modules/home-manager/terminal-redeemer.nix ];
      };
      nixosModules.terminal-redeemer = { ... }: {
        _module.args.terminalRedeemerHomeManagerModule = self.homeManagerModules.terminal-redeemer;
        imports = [ ./modules/nixos/terminal-redeemer.nix ];
      };
      lib.sliceConsumerContract = {
        schemaVersion = 1;
        inventorySchemaVersion = 1;
        rpcSchemaVersion = 1;
        controllerSchemaVersion = 2;
        authorityMode = "host_location";
        leechWriteAuthorized = false;
        contractId = "terminal-redeemer.host-leech-slices";
        contractVersion = "1.2.0";
        niriVersion = "26.04";
        zellijVersion = "0.44.3";
        allEligibleIncludesUnnamed = true;
        allEligibleRoutesLaunches = false;
        leechModeEnabledByDefault = false;
        controllerEnabledByDefault = false;
        sliceClipboardEnabledByDefault = false;
        bindingsInstalledAutomatically = false;
        legacyAttachRetained = true;
        watchSupported = false;
        automaticLocalFallbackAfterRemoteIntent = false;
        artifact = ./contracts/host-leech-slices/v1/consumer-contract.json;
      };
    };
}
