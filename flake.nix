# flake.nix describes a Nix source repository that provides
# development builds of Tailscale and the fork of the Go compiler
# toolchain that Tailscale maintains. It also provides a development
# environment for working on tailscale, for use with "nix develop".
#
# For more information about this and why this file is useful, see:
# https://nixos.wiki/wiki/Flakes
#
# Also look into direnv: https://direnv.net/, this can make it so that you can
# automatically get your environment set up when you change folders into the
# project.
#
# WARNING: currently, the packages provided by this flake are brittle,
# and importing this flake into your own Nix configs is likely to
# leave you with broken builds periodically.
#
# The issue is that building Tailscale binaries uses the buildGoModule
# helper from nixpkgs. This helper demands to know the content hash of
# all of the Go dependencies of this repo, in the form of a Nix SRI
# hash. This hash isn't automatically kept in sync with changes made
# to go.mod yet, and so every time we update go.mod while hacking on
# Tailscale, this flake ends up with a broken build due to hash
# mismatches.
#
# Right now, this flake is intended for use by Tailscale developers,
# who are aware of this mismatch and willing to live with it. At some
# point, we'll add automation to keep the hashes more in sync, at
# which point this caveat should go away.
#
# See https://github.com/tailscale/tailscale/issues/6845 for tracking
# how to fix this mismatch.
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    flake-utils.url = "github:numtide/flake-utils";
    # Used by shell.nix as a compat shim.
    flake-compat = {
      url = "github:edolstra/flake-compat";
      flake = false;
    };
  };

  outputs = { self, nixpkgs, flake-utils, flake-compat }: let
    # Grab a helper func out of the Nix language libraries. Annoyingly
    # these are only accessible through legacyPackages right now,
    # which forces us to indirect through a platform-specific
    # path. The x86_64-linux in here doesn't really matter, since all
    # we're grabbing is a pure Nix string manipulation function that
    # doesn't build any software.
    fileContents = nixpkgs.legacyPackages.x86_64-linux.lib.fileContents;

    tailscale-go-rev = fileContents ./go.toolchain.rev;
    tailscale-go-sri = fileContents ./go.toolchain.sri;

    # pkgsWithTailscaleGo takes a nixpkgs package set, and replaces
    # its Go 1.19 compiler with tailscale's fork.
    #
    # We need to do this because the buildGoModule helper function is
    # constructed with legacy nix imports, so we cannot construct a
    # buildGoModule variant that uses tailscale's toolchain. Instead,
    # we have to replace the toolchain in nixpkgs, and let lazy
    # evaluation propagate it into the nixpkgs instance of
    # buildGoModule.
    #
    # This is a bit roundabout, but there doesn't seem to be a more
    # elegant way of resolving the impedance mismatch between legacy
    # nixpkgs style imports and flake semantics, unless upstream
    # nixpkgs exposes the buildGoModule constructor func explicitly.
    pkgsWithTailscaleGo = pkgs: pkgs.extend (final: prev: rec {
      tailscale_go = prev.lib.overrideDerivation prev.go_1_19 (attrs: rec {
        name = "tailscale-go-${version}";
        version = tailscale-go-rev;
        src = pkgs.fetchFromGitHub {
          owner = "tailscale";
          repo = "go";
          rev = tailscale-go-rev;
          sha256 = tailscale-go-sri;
        };
        nativeBuildInputs = attrs.nativeBuildInputs ++ [ pkgs.git ];
		    # Remove dependency on xcbuild as that causes iOS/macOS builds to fail.
        propagatedBuildInputs = [];
        checkPhase = "";
        TAILSCALE_TOOLCHAIN_REV = tailscale-go-rev;
      });
      # Override go_1_19 so that buildGo119Module below uses
      # tailscale's toolchain as well.
      go_1_19 = tailscale_go;
    });

    # tailscaleRev is the git commit at which this flake was imported,
    # or the empty string when building from a local checkout of the
    # tailscale repo.
    tailscaleRev = if builtins.hasAttr "rev" self then self.rev else "";
    # tailscale takes a nixpkgs package set, and builds Tailscale from
    # the same commit as this flake. IOW, it provides "tailscale built
    # from HEAD", where HEAD is "whatever commit you imported the
    # flake at".
    #
    # This is currently unfortunately brittle, because we have to
    # specify vendorSha256, and that sha changes any time we alter
    # go.mod. We don't want to force a nix dependency on everyone
    # hacking on Tailscale, so this flake is likely to have broken
    # builds periodically until somoene comes through and manually
    # fixes them up. I sure wish there was a way to express "please
    # just trust the local go.mod, vendorSha256 has no benefit here",
    # but alas.
    #
    # So really, this flake is for tailscale devs to dogfood with, if
    # you're an end user you should be prepared for this flake to not
    # build periodically.
    tailscale = pkgs: pkgs.buildGo119Module rec {
      name = "tailscale";

      src = ./.;
      vendorSha256 = fileContents ./go.mod.sri;
      nativeBuildInputs = pkgs.lib.optionals pkgs.stdenv.isLinux [ pkgs.makeWrapper pkgs.git ];
      ldflags = ["-X tailscale.com/version.GitCommit=${tailscaleRev}"];
      CGO_ENABLED = 0;
      subPackages = [ "cmd/tailscale" "cmd/tailscaled" ];
      doCheck = false;
      postInstall = pkgs.lib.optionalString pkgs.stdenv.isLinux ''
        wrapProgram $out/bin/tailscaled --prefix PATH : ${pkgs.lib.makeBinPath [ pkgs.iproute2 pkgs.iptables pkgs.getent pkgs.shadow ]}
        wrapProgram $out/bin/tailscale --suffix PATH : ${pkgs.lib.makeBinPath [ pkgs.procps ]}

        sed -i -e "s#/usr/sbin#$out/bin#" -e "/^EnvironmentFile/d" ./cmd/tailscaled/tailscaled.service
        install -D -m0444 -t $out/lib/systemd/system ./cmd/tailscaled/tailscaled.service
      '';
    };

    # This whole blob makes the tailscale package available for all
    # OS/CPU combos that nix supports, as well as a dev shell so that
    # "nix develop" and "nix-shell" give you a dev env.
    flakeForSystem = nixpkgs: system: let
      upstreamPkgs = nixpkgs.legacyPackages.${system};
      pkgs = pkgsWithTailscaleGo upstreamPkgs;
      ts = tailscale pkgs;
    in {
      packages = {
        tailscale-go = pkgs.tailscale-go;
        tailscale = ts;
      };
      devShell = pkgs.mkShell {
        packages = with upstreamPkgs; [
          curl
          git
          gopls
          gotools
          graphviz
          perl
          pkgs.tailscale_go
        ];
      };
    };
  in
    flake-utils.lib.eachDefaultSystem (system: flakeForSystem nixpkgs system);
}
# nix-direnv cache busting line: sha256-imidcDJGVor43PqdTX7Js4/tjQ0JA2E1GdjuyLiPDHI= sha256-zBfANuVhYtDOOiZu6SPVmM0cTEsOHVdycOz5EZDapKk=
