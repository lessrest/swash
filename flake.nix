{
  description = "busker - Interactive process sessions over D-Bus";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    tagflow = {
      url = "github:lessrest/tagflow";
      flake = false;
    };
  };

  outputs = { self, nixpkgs, tagflow }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      tagflowPkg = pkgs.python3Packages.buildPythonPackage {
        pname = "tagflow";
        version = "0.13.0";
        src = tagflow;
        format = "pyproject";
        nativeBuildInputs = [ pkgs.python3Packages.hatchling ];
        dependencies = with pkgs.python3Packages; [
          anyio
          beautifulsoup4
        ];
        doCheck = false;
      };

      python = pkgs.python3.withPackages (ps: [ ps.sdbus ps.rich ps.mcp ps.anyio ps.systemd-python ]);

      webPython = pkgs.python3.withPackages (ps: [
        ps.uvicorn
        ps.sdbus
        ps.rich
        ps.anyio
        ps.mcp
        ps.systemd-python
        tagflowPkg
      ]);

      busker = pkgs.writeShellApplication {
        name = "busker";
        runtimeInputs = [ python pkgs.lldb ];
        text = ''
          export PYTHONPATH="${./.}:''${PYTHONPATH:-}"
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

      buskerWeb = pkgs.writeShellApplication {
        name = "busker-web";
        runtimeInputs = [ webPython ];
        text = ''
          export PYTHONPATH="${./.}:''${PYTHONPATH:-}"
          exec python3 ${./busker-web.py} "$@"
        '';
      };
    in
    {
      packages.${system} = {
        inherit busker busdap buskerWeb;
        default = busker;
      };

      apps.${system} = {
        default = {
          type = "app";
          program = "${busker}/bin/busker";
        };
        web = {
          type = "app";
          program = "${buskerWeb}/bin/busker-web";
        };
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = [ python webPython pkgs.lldb ];
      };
    };
}
