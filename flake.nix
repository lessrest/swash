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

            vendorHash = "sha256-q1YZecpbrLChbvaHfPzzCeMIuepuF+v+z7dAv1a89gM=";
            subPackages = [ "cmd/swash" ];
            env.GOWORK = "off";

            nativeBuildInputs = pkgs.lib.optionals pkgs.stdenv.isLinux [ pkgs.patchelf ];
            preBuild = ''
              export CGO_CFLAGS="-I$PWD/cvendor''${CGO_CFLAGS:+ $CGO_CFLAGS}"
            '';
            # libsystemd is dlopen'ed, so put it on the binary's own rpath
            # rather than in LD_LIBRARY_PATH, which would leak into every
            # session's environment and be missing in --login sessions.
            postFixup = pkgs.lib.optionalString pkgs.stdenv.isLinux ''
              patchelf --add-rpath ${pkgs.lib.makeLibraryPath [ pkgs.systemd ]} $out/bin/swash
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
              pkgs.systemd
            ];
            CGO_CFLAGS = "-I${self}/cvendor";
            LD_LIBRARY_PATH = pkgs.lib.makeLibraryPath [ pkgs.systemd ];
          };
        });
    };
}
