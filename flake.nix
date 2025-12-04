{
  description = "busdap - Debug Adapter Protocol over D-Bus";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      python = pkgs.python3.withPackages (ps: [ ps.sdbus ps.rich ps.mcp ps.anyio ]);

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
        inherit busdap;
        default = busdap;
      };

      apps.${system}.default = {
        type = "app";
        program = "${busdap}/bin/busdap";
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = [ python pkgs.lldb ];
      };
    };
}
