{
  description = "vterm-service: Terminal emulator over D-Bus";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};

      # Font configuration for fcft to find JetBrains Mono
      fontsConf = pkgs.makeFontsConf {
        fontDirectories = [ pkgs.jetbrains-mono ];
      };
    in
    {
      packages.${system}.default = pkgs.stdenv.mkDerivation {
        pname = "vterm-service";
        version = "0.1.0";

        src = ./.;

        nativeBuildInputs = with pkgs; [
          meson
          ninja
          pkg-config
          makeWrapper
        ];

        buildInputs = with pkgs; [
          sdbus-cpp
          systemdLibs
          fcft
          pixman
          libpng
        ];

        # Make fontconfig find JetBrains Mono at runtime
        postInstall = ''
          wrapProgram $out/bin/vterm-service \
            --set FONTCONFIG_FILE ${fontsConf}
        '';

        meta = {
          description = "Terminal emulator over D-Bus using libvterm";
        };
      };

      devShells.${system}.default = pkgs.mkShell {
        packages = with pkgs; [
          meson
          ninja
          pkg-config
          sdbus-cpp
          systemdLibs
          fcft
          pixman
          libpng
          clang-tools  # for clangd
          jetbrains-mono
        ];

        FONTCONFIG_FILE = fontsConf;
      };
    };
}
