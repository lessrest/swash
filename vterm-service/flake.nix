{
  description = "vterm-service: Terminal emulator over D-Bus";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { self, nixpkgs }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
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
        ];

        buildInputs = with pkgs; [
          sdbus-cpp
          systemdLibs
        ];

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
          clang-tools  # for clangd
        ];
      };
    };
}
