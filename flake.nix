{
  description = "swash - Interactive process sessions over D-Bus";

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

      python = pkgs.python3.withPackages (ps: [ ps.sdbus ps.rich ps.mcp ps.anyio ps.systemd-python ps.anthropic ]);

      webPython = pkgs.python3.withPackages (ps: [
        ps.uvicorn
        ps.sdbus
        ps.rich
        ps.anyio
        ps.mcp
        ps.systemd-python
        tagflowPkg
      ]);

      swash = pkgs.writeShellApplication {
        name = "swash";
        runtimeInputs = [ python pkgs.lldb ];
        text = ''
          export PYTHONPATH="${./.}:''${PYTHONPATH:-}"
          exec python3 ${./swash.py} "$@"
        '';
      };

      swashWeb = pkgs.writeShellApplication {
        name = "swash-web";
        runtimeInputs = [ webPython ];
        text = ''
          export PYTHONPATH="${./.}:''${PYTHONPATH:-}"
          exec python3 ${./swash-web.py} "$@"
        '';
      };
    in
    {
      packages.${system} = {
        inherit swash swashWeb;
        default = swash;
      };

      apps.${system} = {
        default = {
          type = "app";
          program = "${swash}/bin/swash";
        };
        web = {
          type = "app";
          program = "${swashWeb}/bin/swash-web";
        };
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = [
          pkgs.uv
          pkgs.lldb
          pkgs.pkg-config
          pkgs.systemd.dev
        ];
      };
    };
}
