{
  description = "busker - Interactive process sessions over D-Bus";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      python = pkgs.python3.withPackages (ps: [ ps.sdbus ps.rich ps.mcp ps.anyio ]);

      busker = pkgs.writeShellApplication {
        name = "busker";
        runtimeInputs = [ python pkgs.lldb ];
        text = ''
          exec python3 ${./busker.py} "$@"
        '';
      };

      # Keep legacy busdap for now
      busdap = pkgs.writeShellApplication {
        name = "busdap";
        runtimeInputs = [ python pkgs.lldb ];
        text = ''
          exec python3 ${./busdap.py} "$@"
        '';
      };
    in
    {
      packages.${system} = {
        inherit busker busdap;
        default = busker;
      };

      apps.${system}.default = {
        type = "app";
        program = "${busker}/bin/busker";
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = [ python pkgs.lldb ];
      };
    };
}
