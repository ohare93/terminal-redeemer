{ config, lib, pkgs, ... }:
let
  cfg = config.programs.terminal-redeemer;
  settingsFormat = pkgs.formats.yaml { };
  settingsFile = settingsFormat.generate "terminal-redeemer-config.yaml" {
    stateDir = cfg.stateDir;
    profile = cfg.profile;
    capture = {
      enabled = cfg.capture.enable;
      interval = cfg.capture.interval;
      snapshotEvery = cfg.capture.snapshotEvery;
    };
    retention = {
      days = cfg.retention.days;
    };
    processMetadata = {
      whitelist = cfg.processWhitelist;
      whitelistExtra = cfg.processWhitelistExtra;
    };
    restore = {
      appAllowlist = cfg.restore.appAllowlist;
      terminal = {
        command = cfg.terminal.command;
      };
    };
  };
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
        ExecStart = "${lib.getExe cfg.package} capture once --config %h/.config/terminal-redeemer/config.yaml";
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
