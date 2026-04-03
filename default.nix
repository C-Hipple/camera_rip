{ pkgs ? import <nixpkgs> {} }:

let
  inherit (pkgs) lib;

  # Filter function to exclude development artifacts
  sourceFilter = path: type:
    let
      baseName = baseNameOf path;
      relPath = lib.removePrefix (toString ./. + "/") (toString path);
    in
      !(lib.hasPrefix "result" baseName) &&
      !(baseName == ".claude") &&
      !(baseName == "node_modules") &&
      !(relPath == "frontend/build" || lib.hasPrefix "frontend/build/" relPath) &&
      !(baseName == "camera-rip" && type == "regular") &&
      !(relPath == "backend-go/camera-rip") &&
      !(relPath == "backend-go/frontend" || lib.hasPrefix "backend-go/frontend/" relPath) &&
      # Exclude .git
      !(baseName == ".git");

  # Filtered source for the entire project
  filteredSrc = lib.cleanSourceWith {
    src = ./.;
    filter = sourceFilter;
    name = "camera-rip-source";
  };

  # Filtered source for frontend only
  frontendSrc = lib.cleanSourceWith {
    src = ./frontend;
    filter = path: type:
      let baseName = baseNameOf path;
      in !(baseName == "node_modules" || baseName == "build");
    name = "camera-rip-frontend-source";
  };

  frontend = pkgs.buildNpmPackage {
    pname = "camera-rip-frontend";
    version = "0.1.0";

    src = frontendSrc;

    npmDepsHash = "sha256-LCS3US954uohKia7B3WkdgzeRBp3kSV2R/lb0OqJyhM=";

    buildPhase = ''
      runHook preBuild
      npm run build
      runHook postBuild
    '';

    installPhase = ''
      runHook preInstall
      mkdir -p $out
      cp -r build $out/
      runHook postInstall
    '';

    # Disable tests during build (they require interactive environment)
    # Tests can be run separately with: cd frontend && npm test
    doCheck = false;
  };

in pkgs.buildGoModule {
  pname = "camera-rip";
  version = "0.1.0";

  src = filteredSrc;

  # Go module vendoring - you'll need to update this hash
  vendorHash = "sha256-NBnkdx47qhEJXPYDlVgJPtZj+UqBHoso6vTl6wukj9s=";

  modRoot = "./backend-go";

  preBuild = ''
    mkdir -p frontend
    cp -r ${frontend}/build frontend/build
  '';

  # Build only main.go
  subPackages = [ "." ];

  meta = with pkgs.lib; {
    description = "Web-based photo import and selection tool for Canon cameras";
    homepage = "https://github.com/C-Hipple/camera_rip";
    license = licenses.mit;
    maintainers = [];
    platforms = platforms.unix;
  };
}
