{ config, lib, options, ... }:
let
  cfg = config.programs.terminal-redeemer;
  hmAvailable = lib.hasAttrByPath [ "home-manager" "users" ] options;
in {
  options.programs.terminal-redeemer = {
    enable = lib.mkEnableOption "terminal-redeemer user setup via Home Manager";

    users = lib.mkOption {
      type = lib.types.attrsOf lib.types.attrs;
      default = { };
      description = ''
        Per-user Home Manager `programs.terminal-redeemer` configuration.
        Each attribute key is a username.
      '';
      example = lib.literalExpression ''
        {
          alice = {
            stateDir = "/home/alice/.terminal-redeemer";
            capture.interval = "30s";
            restore.appAllowlist.firefox = "firefox --new-window";
            restore.appMode.firefox = "oneshot";
            restore.reconcileWorkspaceMoves = true;
            restore.workspaceReconcileDelay = "1200ms";
            terminal.command = "kitty";
          };
        }
      '';
    };
  };

  config = lib.mkMerge [
    (lib.mkIf (cfg.enable && hmAvailable) {
      home-manager.sharedModules = [ (import ../home-manager/terminal-redeemer.nix) ];
      home-manager.users = lib.mapAttrs (username: userCfg: {
        home.username = lib.mkDefault username;
        home.homeDirectory = lib.mkDefault (config.users.users.${username}.home or "/home/${username}");
        home.stateVersion = lib.mkDefault config.system.stateVersion;
        programs.terminal-redeemer = userCfg // {
          enable = lib.mkDefault true;
        };
      }) cfg.users;
    })

    (lib.mkIf (cfg.enable && !hmAvailable) {
      assertions = [
        {
          assertion = false;
          message = ''
            programs.terminal-redeemer on NixOS requires the Home Manager NixOS module.
            Import `home-manager.nixosModules.home-manager` in your NixOS modules list.
          '';
        }
      ];
    })
  ];
}
