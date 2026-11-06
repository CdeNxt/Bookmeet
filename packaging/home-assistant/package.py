#!/usr/bin/env python3
"""Export the maintained fork for the existing Home Assistant updater."""
import argparse
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent

def git(*args):
    return subprocess.check_output(["git", "-C", str(ROOT), *args])

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, default=ROOT / "release/calnode-bookmeet-arm64")
    parser.add_argument("--output", type=Path, default=ROOT / "release/bookmeet-pi-fix.tar.gz")
    args = parser.parse_args()
    if git("status", "--porcelain").strip():
        raise SystemExit("Commit or stash the project changes before packaging a release.")
    version = (HERE / "version.txt").read_text().strip()
    base = (HERE / "upstream-base.txt").read_text().strip()
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+\+bookmeet\.[0-9]+", version):
        raise SystemExit("Invalid Bookmeet version")
    if not re.fullmatch(r"v[0-9]+\.[0-9]+\.[0-9]+", base):
        raise SystemExit("Invalid upstream base tag")
    if not args.binary.is_file():
        raise SystemExit("Build or supply the tested ARM64 binary first.")
    patch = git("diff", "--binary", base, "HEAD", "--", ".", ":!.github", ":!AGENTS.md", ":!BOOKMEET.md", ":!packaging/home-assistant", ":!branding", ":!.gitignore")
    wrapper = "run-ha-bookmeet-" + version.split("bookmeet.")[-1] + ".sh"
    docker = (HERE / "Dockerfile.in").read_text().replace("__UPSTREAM_VERSION__", base[1:]).replace("__BOOKMEET_PATCH__", version.split("bookmeet.")[-1]).replace("__WRAPPER__", wrapper)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="bookmeet-package-") as tmp:
        folder = Path(tmp)
        (folder / "invite-links.patch").write_bytes(patch)
        (folder / "Dockerfile").write_text(docker)
        with tarfile.open(args.output, "w:gz") as archive:
            archive.add(folder / "invite-links.patch", arcname="invite-links.patch")
            archive.add(folder / "Dockerfile", arcname="Dockerfile")
            archive.add(args.binary, arcname="calnode-bookmeet-arm64")
    print(args.output)
    print("Source commit: " + git("rev-parse", "HEAD").decode().strip())

if __name__ == "__main__":
    main()
