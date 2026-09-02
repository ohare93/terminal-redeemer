{ config, lib, pkgs, ... }:
let
  cfg = config.programs.terminal-redeemer;
  settingsFormat = pkgs.formats.yaml { };
  renderedConfig = {
    stateDir = cfg.stateDir;
    host = cfg.host;
    profile = cfg.profile;
    capture = {
      interval = cfg.capture.interval;
      niriCommand = cfg.capture.niriCommand;
    };
    retention = {
      days = cfg.retention.days;
    };
    processMetadata = {
      whitelist = cfg.processWhitelist;
      whitelistExtra = cfg.processWhitelistExtra;
      includeSessionTag = cfg.processIncludeSessionTag;
    };
    resume = {
      onStartup = cfg.resume.onStartup;
      maxCheckpointAge = cfg.resume.maxCheckpointAge;
      unresolvedWorkspace = cfg.resume.unresolvedWorkspace;
      timeout = cfg.resume.timeout;
      pollInterval = cfg.resume.pollInterval;
      terminalCommand = cfg.resume.terminalCommand;
    };
    mirror = {
      sourceHost = cfg.mirror.sourceHost;
      sshCommand = cfg.mirror.sshCommand;
      sshOptions = cfg.mirror.sshOptions;
      snapshotCommand = cfg.mirror.snapshotCommand;
      launcherCommand = cfg.mirror.launcherCommand;
      selfCommand = cfg.mirror.selfCommand;
      appID = cfg.mirror.appID;
      openDelay = cfg.mirror.openDelay;
      niriCommand = cfg.mirror.niriCommand;
      clipboard = {
        enabled = cfg.mirror.clipboard.enabled;
        command = cfg.mirror.clipboard.command;
        scpCommand = cfg.mirror.clipboard.scpCommand;
        scpOptions = cfg.mirror.clipboard.scpOptions;
        kittyCommand = cfg.mirror.clipboard.kittyCommand;
        tempDir = cfg.mirror.clipboard.tempDir;
        mimeTypes = cfg.mirror.clipboard.mimeTypes;
      };
    };
  };
  settingsFile = settingsFormat.generate "terminal-redeemer-config.yaml" renderedConfig;
  configPath = "${config.xdg.configHome}/terminal-redeemer/config.yaml";
  captureExecStart = "${lib.getExe cfg.package} --config ${lib.escapeShellArg configPath} capture once";
  resumeExecStart = "${lib.getExe cfg.package} --config ${lib.escapeShellArg configPath} resume --all";
  pruneExecStart = "${lib.getExe cfg.package} --config ${lib.escapeShellArg configPath} prune run";
  renderNiriSpawn = argv: "spawn " + lib.concatMapStringsSep " " builtins.toJSON argv + ";";
  renderNiriStartup = argv: "spawn-at-startup " + lib.concatMapStringsSep " " builtins.toJSON argv + ";";
  resumeNiriIntegrationFragment = lib.optionalString cfg.resume.onStartup ''
    // Restart the same bounded Home Manager recovery unit on every Niri start.
    ${renderNiriStartup [ "${pkgs.systemd}/bin/systemctl" "--user" "restart" "terminal-redeemer-resume.service" ]}
  '';
  mirrorLocalCommand = [ cfg.mirror.launcherCommand ];
  mirrorHostArgs = lib.optionals (cfg.mirror.sourceHost != "") [ "--host" cfg.mirror.sourceHost ];
  mirrorNewCommand = [ (lib.getExe cfg.package) "mirror" "new" ]
    ++ mirrorHostArgs
    ++ lib.optionals (cfg.mirror.sourceWorkspace != "") [ "--source-workspace" cfg.mirror.sourceWorkspace ];
  mirrorOpenCommand = [ (lib.getExe cfg.package) "mirror" "open" ] ++ mirrorHostArgs;
  mirrorSaveCommand = [ (lib.getExe cfg.package) "mirror" "save" ] ++ mirrorHostArgs;
  mirrorApplyCommand = [ (lib.getExe cfg.package) "mirror" "apply" ] ++ mirrorHostArgs;
  mirrorFollowCommand = [ (lib.getExe cfg.package) "mirror" "follow" ] ++ mirrorHostArgs;
  mirrorNiriIntegrationFragment = ''
    // Terminal Redeemer explicit local and remote terminal shortcuts.
    // Opt-in template only; this module does not install these bindings.
    binds {
        Mod+Return { ${renderNiriSpawn mirrorLocalCommand} }
        Mod+Shift+Return { ${renderNiriSpawn mirrorNewCommand} }
        Mod+Ctrl+Return { ${renderNiriSpawn mirrorOpenCommand} }
    }
  '';
  graphicalPath = "${config.home.profileDirectory}/bin:/run/current-system/sw/bin";
in {
  options.programs.terminal-redeemer = {
    enable = lib.mkEnableOption "terminal-redeemer";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.terminal-redeemer or (pkgs.writeShellScriptBin "redeem" ''
        echo "terminal-redeemer package is not configured" >&2
        exit 1
      '');
      defaultText = lib.literalExpression "pkgs.terminal-redeemer";
      description = "Package providing the redeem CLI.";
    };

    stateDir = lib.mkOption {
      type = lib.types.str;
      default = "${config.home.homeDirectory}/.terminal-redeemer";
      description = "Root state directory.";
    };

    host = lib.mkOption {
      type = lib.types.str;
      default = "local";
      description = "Host partition key for rolling boot checkpoints.";
    };

    profile = lib.mkOption {
      type = lib.types.str;
      default = "default";
      description = "Profile segment under host partition.";
    };

    capture = {
      enable = lib.mkOption {
        type = lib.types.bool;
        default = true;
        description = "Enable capture timer/service.";
      };

      interval = lib.mkOption {
        type = lib.types.str;
        default = "60s";
        description = "Capture interval.";
      };

      niriCommand = lib.mkOption {
        type = lib.types.str;
        default = "niri msg -j windows";
        description = "Command used on every capture interval to query complete Niri window/workspace JSON.";
      };
    };

    retention.days = lib.mkOption {
      type = lib.types.int;
      default = 30;
      description = "Retention period in days.";
    };

    retention.prune.enable = lib.mkEnableOption "terminal-redeemer retention prune timer";

    retention.prune.onCalendar = lib.mkOption {
      type = lib.types.str;
      default = "daily";
      description = "Calendar expression for retention prune schedule.";
    };

    processWhitelist = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "opencode" "claude" ];
      description = "Default process names to annotate.";
    };

    processWhitelistExtra = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ ];
      description = "Extra process names to annotate.";
    };

    processIncludeSessionTag = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Whether to include session tag extraction for terminals.";
    };

    resume = {
      onStartup = lib.mkOption {
        type = lib.types.bool;
        default = false;
        description = ''
          Run the canonical `redeem resume --all` command at graphical-session startup.
          Add `resume.niriIntegrationFragment` once to Niri's configuration so
          compositor restarts invoke the same service. Keep this disabled until
          any host-local startup restoration is disabled.
        '';
      };
      niriIntegrationFragment = lib.mkOption {
        type = lib.types.lines;
        readOnly = true;
        default = resumeNiriIntegrationFragment;
        description = "Generated Niri spawn-at-startup hook that restarts the Home Manager recovery service on every compositor start; empty when startup recovery is disabled.";
      };
      maxCheckpointAge = lib.mkOption { type = lib.types.str; default = "24h"; description = "Maximum age accepted by prior-boot resume."; };
      unresolvedWorkspace = lib.mkOption { type = lib.types.enum [ "skip" "current" "fail" ]; default = "current"; description = "Policy when a captured workspace cannot be resolved."; };
      timeout = lib.mkOption { type = lib.types.str; default = "10s"; description = "Bound for each resume readiness, correlation, attachment, and move-verification phase."; };
      pollInterval = lib.mkOption { type = lib.types.str; default = "100ms"; description = "Polling interval for exact Niri and Zellij evidence."; };
      terminalCommand = lib.mkOption { type = lib.types.str; default = "kitty"; description = "Direct Kitty executable used by resume."; };
    };

    mirror = {
      localCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = mirrorLocalCommand; description = "Direct local Kitty argv suitable for Mod+Return."; };
      newCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = mirrorNewCommand; description = "Packaged argv creating one persistent remote session and best-effort opening its source view."; };
      openCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = mirrorOpenCommand; description = "Packaged argv opening the manual remote session picker."; };
      saveCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = mirrorSaveCommand; description = "Packaged argv replacing the pinned manual projection set from fresh evidence."; };
      applyCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = mirrorApplyCommand; description = "Packaged argv applying the pinned manual projection set attach-only."; };
      followCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = mirrorFollowCommand; description = "Packaged argv starting the temporary foreground workspace-follow TUI."; };
      niriIntegrationFragment = lib.mkOption { type = lib.types.lines; readOnly = true; default = mirrorNiriIntegrationFragment; description = "Generated opt-in Niri shortcuts for local, new remote, and remote picker terminals; never installed automatically."; };
      sourceHost = lib.mkOption { type = lib.types.str; default = ""; description = "SSH source host for live session mirroring."; };
      sourceWorkspace = lib.mkOption { type = lib.types.str; default = ""; description = "Optional source Niri workspace name or number passed only to mirror new; empty preserves normal source placement."; };
      sshCommand = lib.mkOption { type = lib.types.str; default = "ssh"; description = "SSH executable."; };
      sshOptions = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ ]; description = "SSH argv options."; };
      snapshotCommand = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ "redeem" "mirror" "snapshot" ]; description = "Remote snapshot command argv."; };
      launcherCommand = lib.mkOption { type = lib.types.str; default = "kitty"; description = "Kitty-compatible local launcher executable."; };
      selfCommand = lib.mkOption { type = lib.types.str; default = "redeem"; description = "Redeem executable used in Kitty clipboard mappings."; };
      appID = lib.mkOption { type = lib.types.str; default = "terminal-redeemer-mirror"; description = "App ID/class marking Terminal Redeemer-owned mirror windows."; };
      openDelay = lib.mkOption { type = lib.types.str; default = "150ms"; description = "Delay between local window launches."; };
      niriCommand = lib.mkOption { type = lib.types.str; default = "niri"; description = "Niri executable for owned-window operations."; };
      clipboard = {
        enabled = lib.mkOption { type = lib.types.bool; default = true; description = "Enable mirrored image-paste bridge mapping."; };
        command = lib.mkOption { type = lib.types.str; default = "wl-paste"; description = "Wayland clipboard reader executable."; };
        scpCommand = lib.mkOption { type = lib.types.str; default = "scp"; description = "SCP executable."; };
        scpOptions = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ ]; description = "SCP argv options."; };
        kittyCommand = lib.mkOption { type = lib.types.str; default = "kitty"; description = "Kitty remote-control executable."; };
        tempDir = lib.mkOption { type = lib.types.str; default = "/tmp"; description = "Absolute temporary path shared with the source host."; };
        mimeTypes = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ "image/png" "image/jpeg" "image/webp" "image/gif" ]; description = "Preferred supported clipboard image MIME types."; };
      };
    };

    renderedConfig = lib.mkOption {
      type = lib.types.attrs;
      visible = false;
      default = { };
      description = "Internal rendered runtime config for eval checks.";
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = [{
      assertion = cfg.mirror.sourceWorkspace == "" || cfg.mirror.sourceHost != "";
      message = "programs.terminal-redeemer.mirror.sourceWorkspace requires mirror.sourceHost";
    }];

    home.packages = [ cfg.package ];
    programs.terminal-redeemer.renderedConfig = renderedConfig;

    xdg.configFile."terminal-redeemer/config.yaml".source = settingsFile;

    systemd.user.services.terminal-redeemer-capture = lib.mkIf cfg.capture.enable {
      Unit = {
        Description = "terminal-redeemer complete Niri state capture";
        After = [ "graphical-session.target" ]
          ++ lib.optional cfg.resume.onStartup "terminal-redeemer-resume.service";
        PartOf = [ "graphical-session.target" ];
      };
      Service = {
        Type = "oneshot";
        ExecStart = captureExecStart;
      };
    };

    systemd.user.services.terminal-redeemer-resume = lib.mkIf cfg.resume.onStartup {
      Unit = {
        Description = "terminal-redeemer prior-boot terminal resume";
        After = [ "graphical-session.target" ];
        Before = [ "terminal-redeemer-capture.service" ];
        PartOf = [ "graphical-session.target" ];
        StartLimitIntervalSec = "30s";
        StartLimitBurst = 5;
      };
      Service = {
        Type = "oneshot";
        ExecStart = resumeExecStart;
        Environment = [ "PATH=${graphicalPath}" ];
        Restart = "on-failure";
        RestartSec = "3s";
      };
      Install.WantedBy = [ "graphical-session.target" ];
    };

    systemd.user.timers.terminal-redeemer-capture = lib.mkIf cfg.capture.enable {
      Unit = {
        Description = "terminal-redeemer periodic complete state capture";
        After = [ "graphical-session.target" ]
          ++ lib.optional cfg.resume.onStartup "terminal-redeemer-resume.service";
        PartOf = [ "graphical-session.target" ];
      };
      Timer = {
        OnActiveSec = cfg.capture.interval;
        OnUnitActiveSec = cfg.capture.interval;
        Unit = "terminal-redeemer-capture.service";
      };
      Install.WantedBy = [ "graphical-session.target" ];
    };

    systemd.user.services.terminal-redeemer-prune = lib.mkIf cfg.retention.prune.enable {
      Unit = {
        Description = "terminal-redeemer retention prune";
      };
      Service = {
        Type = "oneshot";
        ExecStart = pruneExecStart;
      };
    };

    systemd.user.timers.terminal-redeemer-prune = lib.mkIf cfg.retention.prune.enable {
      Unit = {
        Description = "terminal-redeemer retention prune schedule";
      };
      Timer = {
        OnCalendar = cfg.retention.prune.onCalendar;
        Persistent = true;
        Unit = "terminal-redeemer-prune.service";
      };
      Install.WantedBy = [ "timers.target" ];
    };
  };
}
