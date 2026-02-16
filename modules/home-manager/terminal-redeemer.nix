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
      snapshotEvery = cfg.capture.snapshotEvery;
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
    restore = {
      appAllowlist = cfg.restore.appAllowlist;
      appMode = cfg.restore.appMode;
      reconcileWorkspaceMoves = cfg.restore.reconcileWorkspaceMoves;
      workspaceReconcileDelay = cfg.restore.workspaceReconcileDelay;
      terminal = {
        command = cfg.terminal.command;
        zellijAttachOrCreate = cfg.terminal.zellijAttachOrCreate;
      };
    };
  } // cfg.extraConfig;
  settingsFile = settingsFormat.generate "terminal-redeemer-config.yaml" renderedConfig;
  configPath = "${config.xdg.configHome}/terminal-redeemer/config.yaml";
  captureExecStart = "${lib.getExe cfg.package} --config ${lib.escapeShellArg configPath} capture once";
  pruneExecStart = "${lib.getExe cfg.package} --config ${lib.escapeShellArg configPath} prune run";
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
      description = "Host partition key for event storage.";
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

      snapshotEvery = lib.mkOption {
        type = lib.types.int;
        default = 100;
        description = "Write snapshot every N events.";
      };

      niriCommand = lib.mkOption {
        type = lib.types.str;
        default = "niri msg -j windows";
        description = "Command used to collect Niri JSON snapshots.";
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

    restore.appAllowlist = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      description = "App ID to spawn command mapping for restore.";
    };

    restore.appMode = lib.mkOption {
      type = lib.types.attrsOf lib.types.str;
      default = { };
      description = "App ID to restore mode mapping (for example: per_window or oneshot).";
    };

    restore.reconcileWorkspaceMoves = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Reconcile Niri workspace moves after restore execution.";
    };

    restore.workspaceReconcileDelay = lib.mkOption {
      type = lib.types.str;
      default = "1200ms";
      description = "Delay before workspace move reconciliation runs.";
    };

    terminal.command = lib.mkOption {
      type = lib.types.str;
      default = "kitty";
      description = "Terminal command used during restore.";
    };

    terminal.zellijAttachOrCreate = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Use zellij attach-or-create strategy during restore.";
    };

    extraConfig = lib.mkOption {
      type = lib.types.attrs;
      default = { };
      description = "Additional raw config merged into rendered YAML.";
    };

    renderedConfig = lib.mkOption {
      type = lib.types.attrs;
      visible = false;
      default = { };
      description = "Internal rendered runtime config for eval checks.";
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];
    programs.terminal-redeemer.renderedConfig = renderedConfig;

    xdg.configFile."terminal-redeemer/config.yaml".source = settingsFile;

    systemd.user.services.terminal-redeemer-capture = lib.mkIf cfg.capture.enable {
      Unit = {
        Description = "terminal-redeemer capture";
      };
      Service = {
        Type = "oneshot";
        ExecStart = captureExecStart;
      };
    };

    systemd.user.timers.terminal-redeemer-capture = lib.mkIf cfg.capture.enable {
      Unit = {
        Description = "terminal-redeemer periodic capture";
      };
      Timer = {
        OnBootSec = "1m";
        OnUnitActiveSec = cfg.capture.interval;
        Unit = "terminal-redeemer-capture.service";
      };
      Install.WantedBy = [ "timers.target" ];
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
