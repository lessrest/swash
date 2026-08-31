{
  description = "Swash process sessions";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          buildGoModule = pkgs.buildGoModule.override { go = pkgs.go_1_25; };
        in
        rec {
          swash = buildGoModule {
            pname = "swash";
            version = "0.1.0-unstable";
            src = self;

            vendorHash = "sha256-uT/BAWjFhauqnf0KuaDf//YCF62setNh5x0c/TEjDrg=";
            subPackages = [ "cmd/swash" ];
            env.GOWORK = "off";

            nativeBuildInputs = [ pkgs.makeWrapper ];
            preBuild = ''
              export CGO_CFLAGS="-I$PWD/cvendor''${CGO_CFLAGS:+ $CGO_CFLAGS}"
            '';
            postFixup = ''
              wrapProgram $out/bin/swash \
                --prefix LD_LIBRARY_PATH : ${pkgs.lib.makeLibraryPath [ pkgs.systemdLibs ]}
            '';

            meta = {
              description = "Run commands as persistent, controllable process sessions";
              homepage = "https://github.com/lessrest/swash";
              mainProgram = "swash";
              platforms = systems;
            };
          };

          default = swash;
        });

      devShells = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        {
          default = pkgs.mkShell {
            packages = [
              pkgs.go_1_25
              pkgs.gnumake
              pkgs.gcc
              pkgs.pkg-config
              pkgs.systemdLibs
            ];
            CGO_CFLAGS = "-I${self}/cvendor";
            LD_LIBRARY_PATH = pkgs.lib.makeLibraryPath [ pkgs.systemdLibs ];
          };
        });
    };
}
