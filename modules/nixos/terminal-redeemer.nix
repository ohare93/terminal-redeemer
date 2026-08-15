{ config, lib, options, terminalRedeemerHomeManagerModule ? import ../home-manager/terminal-redeemer.nix, ... }:
let
  cfg = config.programs.terminal-redeemer;
  hmAvailable = lib.hasAttrByPath [ "home-manager" "users" ] options;
in {
  options.programs.terminal-redeemer = {
    enable = lib.mkEnableOption "terminal-redeemer user setup via Home Manager";

    users = lib.mkOption {
      type = lib.types.attrsOf (lib.types.submodule {
        freeformType = lib.types.attrs;
        options.resume = lib.mkOption {
          type = lib.types.submodule {
            freeformType = lib.types.attrs;
            options.onStartup = lib.mkOption {
              type = lib.types.bool;
              default = false;
              description = ''
                Run the Home Manager-owned graphical-session resume service for this user.
                Disable host-local startup restoration before enabling this option.
              '';
            };
          };
          default = { };
        };
      });
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
            resume.onStartup = true;
            resume.terminalCommand = "kitty";
          };
        }
      '';
    };
  };

  config = lib.mkMerge [
    (lib.mkIf (cfg.enable && hmAvailable) {
      home-manager.sharedModules = [ terminalRedeemerHomeManagerModule ];
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
