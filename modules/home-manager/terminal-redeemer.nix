{ config, lib, pkgs, terminalRedeemerNiri ? pkgs.niri, terminalRedeemerZellij ? pkgs.zellij, ... }:
let
  cfg = config.programs.terminal-redeemer;
  supportedNiriVersion = "26.04";
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
      onStartup = cfg.restore.onStartup;
      appAllowlist = cfg.restore.appAllowlist;
      appMode = cfg.restore.appMode;
      reconcileWorkspaceMoves = cfg.restore.reconcileWorkspaceMoves;
      workspaceReconcileDelay = cfg.restore.workspaceReconcileDelay;
      maxCheckpointAge = cfg.restore.maxCheckpointAge;
      unresolvedWorkspace = cfg.restore.unresolvedWorkspace;
      resumeTimeout = cfg.restore.resumeTimeout;
      resumePollInterval = cfg.restore.resumePollInterval;
      terminal = {
        command = cfg.terminal.command;
        zellijAttachOrCreate = cfg.terminal.zellijAttachOrCreate;
      };
    };
    slice = {
      leechModeEnabled = cfg.slice.leechMode.enable;
      sourceHost = cfg.slice.sourceHost;
      selfCommand = cfg.slice.selfCommand;
      kittyCommand = cfg.slice.kittyCommand;
      transportCommand = cfg.slice.transportCommand;
      transportOptions = cfg.slice.transportOptions;
      rpcCommand = cfg.slice.rpcCommand;
      zellijCommand = cfg.slice.zellijCommand;
      niriCommand = cfg.slice.niriCommand;
      systemctlCommand = cfg.slice.systemctlCommand;
      expectedNiriVersion = supportedNiriVersion;
      requestTimeout = cfg.slice.requestTimeout;
      keepaliveInterval = cfg.slice.keepaliveInterval;
      keepaliveCount = cfg.slice.keepaliveCount;
      retryMaxAttempts = cfg.slice.retryMaxAttempts;
      retryInitialBackoff = cfg.slice.retryInitialBackoff;
      retryMaxBackoff = cfg.slice.retryMaxBackoff;
      attachPrivateRoot = cfg.slice.attachPrivateRoot;
      attachShimCache = cfg.slice.attachShimCache;
      graphicalContextKeys = [ "NIRI_SOCKET" "WAYLAND_DISPLAY" "XDG_RUNTIME_DIR" ];
      clipboard.enabled = false;
      controller = {
        enabled = cfg.slice.controller.enable;
        hostID = cfg.slice.controller.hostID;
        leechID = cfg.slice.controller.leechID;
        pollInterval = cfg.slice.controller.pollInterval;
        controlTimeout = cfg.slice.controller.controlTimeout;
        retryWindow = cfg.slice.controller.retryWindow;
        sourceGoneGrace = cfg.slice.controller.sourceGoneGrace;
        sourceGoneConfirmations = cfg.slice.controller.sourceGoneConfirmations;
        authorityMode = "host_location";
        leechWriteAuthorized = false;
      };
    };
    mirror = {
      sourceHost = cfg.mirror.sourceHost;
      sshCommand = cfg.mirror.sshCommand;
      sshOptions = cfg.mirror.sshOptions;
      snapshotCommand = cfg.mirror.snapshotCommand;
      launcherCommand = cfg.mirror.launcherCommand;
      selfCommand = cfg.mirror.selfCommand;
      appID = cfg.mirror.appID;
      defaultMode = cfg.mirror.defaultMode;
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
  } // cfg.extraConfig;
  settingsFile = settingsFormat.generate "terminal-redeemer-config.yaml" renderedConfig;
  configPath = "${config.xdg.configHome}/terminal-redeemer/config.yaml";
  captureExecStart = "${lib.getExe cfg.package} --config ${lib.escapeShellArg configPath} capture once";
  resumeExecStart = "${lib.getExe cfg.package} --config ${lib.escapeShellArg configPath} resume";
  pruneExecStart = "${lib.getExe cfg.package} --config ${lib.escapeShellArg configPath} prune run";
  controllerExecStart = "${lib.getExe cfg.package} --config ${lib.escapeShellArg configPath} slice controller run";
  renderNiriSpawn = argv: "spawn " + lib.concatMapStringsSep " " builtins.toJSON argv + ";";
  mirrorLocalCommand = [ cfg.mirror.launcherCommand ];
  mirrorNewCommand = [ (lib.getExe cfg.package) "mirror" "new" ]
    ++ lib.optionals (cfg.mirror.sourceHost != "") [ "--host" cfg.mirror.sourceHost ];
  mirrorOpenCommand = [ (lib.getExe cfg.package) "mirror" "open" ]
    ++ lib.optionals (cfg.mirror.sourceHost != "") [ "--host" cfg.mirror.sourceHost ];
  mirrorNiriIntegrationFragment = ''
    // Terminal Redeemer explicit local and remote terminal shortcuts.
    // Opt-in template only; this module does not install these bindings.
    binds {
        Mod+Return { ${renderNiriSpawn mirrorLocalCommand} }
        Mod+Shift+Return { ${renderNiriSpawn mirrorNewCommand} }
        Mod+Ctrl+Return { ${renderNiriSpawn mirrorOpenCommand} }
    }
  '';
  sliceLaunchCommand = [ (lib.getExe cfg.package) "slice" "launch" ];
  sliceCloseFocusedCommand = [ (lib.getExe cfg.package) "slice" "close-focused" ];
  sliceManageCommand = [
    cfg.slice.kittyCommand
    "--config" "NONE"
    "--class" "terminal-redeemer-slice-manager"
    "--override" "confirm_os_window_close=0"
    "--title" "Terminal Redeemer Slices"
    "-e" cfg.slice.selfCommand "--config" configPath "slice" "manage"
  ];
  sliceNiriIntegrationFragment = mirrorNiriIntegrationFragment;
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
        description = "Write a legacy timestamped replay snapshot every N changed events; rolling per-boot checkpoints refresh after every successful complete capture.";
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

    restore.onStartup = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = ''
        Run the canonical `redeem resume` command at graphical-session startup.
        Keep this disabled until any host-local startup restoration is disabled.
      '';
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

    restore.maxCheckpointAge = lib.mkOption {
      type = lib.types.str;
      default = "24h";
      description = "Maximum age accepted by implicit prior-boot resume.";
    };

    restore.unresolvedWorkspace = lib.mkOption {
      type = lib.types.enum [ "skip" "current" "fail" ];
      default = "current";
      description = "Policy when a captured workspace cannot be resolved in current Niri state.";
    };

    restore.resumeTimeout = lib.mkOption {
      type = lib.types.str;
      default = "10s";
      description = "Bound for each resume correlation, attachment, and move-verification phase.";
    };

    restore.resumePollInterval = lib.mkOption {
      type = lib.types.str;
      default = "100ms";
      description = "Polling interval while resume waits for exact Niri and Zellij evidence.";
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

    slice = {
      leechMode.enable = lib.mkEnableOption "explicit routed Leech terminal-launch mode (disabled by default; runtime mode can also be inspected/toggled with redeem slice mode)";
      launchCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = sliceLaunchCommand; description = "Packaged argv suitable for a future leech Niri Super+Enter binding; this module does not install the binding."; };
      closeFocusedCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = sliceCloseFocusedCommand; description = "Packaged argv suitable for a future leech Niri Super+W projection-close binding; this module does not install the binding."; };
      manageCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = sliceManageCommand; description = "Direct packaged Kitty argv that opens the live slice manager; consumers choose and install any binding."; };
      niriIntegrationFragment = lib.mkOption { type = lib.types.lines; readOnly = true; default = sliceNiriIntegrationFragment; description = "Deprecated alias of the explicit local/mirror Niri fragment; never installed automatically."; };
      sourceHost = lib.mkOption { type = lib.types.str; default = ""; description = "Operator-owned SSH destination for the additive slice RPC transport."; };
      selfCommand = lib.mkOption { type = lib.types.str; default = lib.getExe cfg.package; defaultText = lib.literalExpression "lib.getExe cfg.package"; description = "Packaged redeem executable; executed directly without a shell wrapper."; };
      kittyCommand = lib.mkOption { type = lib.types.str; default = lib.getExe pkgs.kitty; defaultText = lib.literalExpression "lib.getExe pkgs.kitty"; description = "Pinned packaged Kitty executable for host launches."; };
      transportCommand = lib.mkOption { type = lib.types.str; default = lib.getExe pkgs.openssh; defaultText = lib.literalExpression "lib.getExe pkgs.openssh"; description = "Packaged SSH transport executable. Authentication and host keys remain operator-owned."; };
      transportOptions = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ ]; description = "Validated operator-owned SSH argv; no host-key or authentication defaults are weakened by the module."; };
      rpcCommand = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ (lib.getExe cfg.package) "slice" "rpc" ]; defaultText = lib.literalExpression ''[ (lib.getExe cfg.package) "slice" "rpc" ]''; description = "Fixed shell-inert remote RPC command argv."; };
      zellijCommand = lib.mkOption { type = lib.types.str; default = lib.getExe terminalRedeemerZellij; defaultText = lib.literalExpression "lib.getExe terminalRedeemerZellij"; description = "Pinned Zellij 0.44.3 executable for exact live-only attachment."; };
      niriCommand = lib.mkOption { type = lib.types.str; default = lib.getExe terminalRedeemerNiri; defaultText = lib.literalExpression "lib.getExe terminalRedeemerNiri"; description = "Pinned Niri 26.04 executable used only for compatibility checks; IPC is direct."; };
      systemctlCommand = lib.mkOption { type = lib.types.str; default = lib.getExe' pkgs.systemd "systemctl"; defaultText = lib.literalExpression ''lib.getExe' pkgs.systemd "systemctl"''; description = "Packaged systemctl used to read only allowlisted graphical user-manager environment keys."; };
      requestTimeout = lib.mkOption { type = lib.types.str; default = "15s"; description = "Positive bound for one RPC request."; };
      keepaliveInterval = lib.mkOption { type = lib.types.str; default = "15s"; description = "Positive SSH transport keepalive interval."; };
      keepaliveCount = lib.mkOption { type = lib.types.ints.between 1 10; default = 3; description = "Bounded SSH keepalive failure count."; };
      retryMaxAttempts = lib.mkOption { type = lib.types.ints.between 1 10; default = 3; description = "Bounded transport attempts for read-only/idempotent queries."; };
      retryInitialBackoff = lib.mkOption { type = lib.types.str; default = "200ms"; description = "Positive initial transport retry delay."; };
      retryMaxBackoff = lib.mkOption { type = lib.types.str; default = "2s"; description = "Positive maximum transport retry delay."; };
      attachPrivateRoot = lib.mkOption { type = lib.types.str; default = ""; description = "Optional absolute same-filesystem dedicated marked attachment root; empty derives and initializes it under the live socket base."; };
      attachShimCache = lib.mkOption { type = lib.types.str; default = ""; description = "Optional absolute empty Zellij resurrection-isolation cache."; };
      clipboard.enabled = lib.mkOption { type = lib.types.bool; readOnly = true; default = false; description = "Clipboard transfer is disabled for the first slice-controller rollout, independently of legacy mirror clipboard."; };
      controller = {
        enable = lib.mkEnableOption "the opt-in foreground terminal-slice reconciliation controller";
        hostID = lib.mkOption { type = lib.types.str; default = "host"; description = "Durable host namespace identity."; };
        leechID = lib.mkOption { type = lib.types.str; default = "leech"; description = "Durable leech namespace identity."; };
        pollInterval = lib.mkOption { type = lib.types.str; default = "2s"; description = "Positive bounded full-snapshot polling interval."; };
        controlTimeout = lib.mkOption { type = lib.types.str; default = "5s"; description = "Positive local control-socket request bound."; };
        retryWindow = lib.mkOption { type = lib.types.str; default = "30s"; description = "Finite reconnect budget preserved across restart."; };
        sourceGoneGrace = lib.mkOption { type = lib.types.str; default = "5s"; description = "Finite complete-authority disappearance grace."; };
        sourceGoneConfirmations = lib.mkOption { type = lib.types.ints.between 2 20; default = 2; description = "Consecutive accepted complete absences required for early confirmation."; };
      };
    };

    mirror = {
      localCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = mirrorLocalCommand; description = "Direct local Kitty argv suitable for Mod+Return."; };
      newCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = mirrorNewCommand; description = "Packaged argv creating and attaching a new persistent remote session."; };
      openCommand = lib.mkOption { type = lib.types.listOf lib.types.str; readOnly = true; default = mirrorOpenCommand; description = "Packaged argv opening the remote session picker."; };
      niriIntegrationFragment = lib.mkOption { type = lib.types.lines; readOnly = true; default = mirrorNiriIntegrationFragment; description = "Generated opt-in Niri shortcuts for local, new remote, and remote picker terminals; never installed automatically."; };
      sourceHost = lib.mkOption { type = lib.types.str; default = ""; description = "SSH source host for live session mirroring."; };
      sshCommand = lib.mkOption { type = lib.types.str; default = "ssh"; description = "SSH executable."; };
      sshOptions = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ ]; description = "SSH argv options."; };
      snapshotCommand = lib.mkOption { type = lib.types.listOf lib.types.str; default = [ "redeem" "mirror" "snapshot" ]; description = "Remote snapshot command argv."; };
      launcherCommand = lib.mkOption { type = lib.types.str; default = "kitty"; description = "Kitty-compatible local launcher executable."; };
      selfCommand = lib.mkOption { type = lib.types.str; default = "redeem"; description = "Redeem executable used in Kitty clipboard mappings."; };
      appID = lib.mkOption { type = lib.types.str; default = "terminal-redeemer-mirror"; description = "App ID/class marking Terminal Redeemer-owned mirror windows."; };
      defaultMode = lib.mkOption { type = lib.types.enum [ "attach" "watch" ]; default = "attach"; description = "Default Zellij mirror mode."; };
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
        Description = "terminal-redeemer complete Niri state capture";
        After = [ "graphical-session.target" ]
          ++ lib.optional cfg.restore.onStartup "terminal-redeemer-resume.service";
        PartOf = [ "graphical-session.target" ];
      };
      Service = {
        Type = "oneshot";
        ExecStart = captureExecStart;
      };
    };

    systemd.user.services.terminal-redeemer-resume = lib.mkIf cfg.restore.onStartup {
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

    systemd.user.services.terminal-redeemer-slice-controller = lib.mkIf cfg.slice.controller.enable {
      Unit = {
        Description = "terminal-redeemer host/leech slice controller";
        After = [ "graphical-session.target" ];
        PartOf = [ "graphical-session.target" ];
        StartLimitIntervalSec = "30s";
        StartLimitBurst = 5;
      };
      Service = {
        Type = "simple";
        ExecStart = controllerExecStart;
        Restart = "on-failure";
        RestartSec = "3s";
      };
      Install.WantedBy = [ "graphical-session.target" ];
    };

    systemd.user.timers.terminal-redeemer-capture = lib.mkIf cfg.capture.enable {
      Unit = {
        Description = "terminal-redeemer periodic complete state capture";
        After = [ "graphical-session.target" ];
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
