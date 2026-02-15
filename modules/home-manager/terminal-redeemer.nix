{ config, lib, pkgs, ... }:
let
  cfg = config.programs.terminal-redeemer;
  settingsFormat = pkgs.formats.yaml { };
  settingsFile = settingsFormat.generate "terminal-redeemer-config.yaml" {
    stateDir = cfg.stateDir;
    host = cfg.host;
    profile = cfg.profile;
    capture = {
      enabled = cfg.capture.enable;
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
      terminal = {
        command = cfg.terminal.command;
        zellijAttachOrCreate = cfg.terminal.zellijAttachOrCreate;
      };
    };
  } // cfg.extraConfig;
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
        default = "niri msg -j workspaces windows";
        description = "Command used to collect Niri JSON snapshots.";
      };
    };

    retention.days = lib.mkOption {
      type = lib.types.int;
      default = 30;
      description = "Retention period in days.";
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
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    xdg.configFile."terminal-redeemer/config.yaml".source = settingsFile;

    systemd.user.services.terminal-redeemer-capture = lib.mkIf cfg.capture.enable {
      Unit = {
        Description = "terminal-redeemer capture";
      };
      Service = {
        Type = "oneshot";
        ExecStart = ''
          ${lib.getExe cfg.package} capture once \
            --state-dir ${lib.escapeShellArg cfg.stateDir} \
            --host ${lib.escapeShellArg cfg.host} \
            --profile ${lib.escapeShellArg cfg.profile} \
            --snapshot-every ${toString cfg.capture.snapshotEvery} \
            --niri-cmd ${lib.escapeShellArg cfg.capture.niriCommand} \
            --process-whitelist-extra ${lib.escapeShellArg (lib.concatStringsSep "," cfg.processWhitelistExtra)} \
            --include-session-tag=${lib.boolToString cfg.processIncludeSessionTag}
        '';
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
  };
}
