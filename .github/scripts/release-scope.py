#!/usr/bin/env python3
"""Classify version-only Release Please PRs without installing project dependencies."""
import json
import os
from pathlib import Path
import re
import subprocess


def read(revision, path):
    return subprocess.check_output(["git", "show", f"{revision}:{path}"], text=True)


def metadata_only(base, head):
    config = json.loads(read(base, "release-please-config.json"))["packages"]["."]
    kind = config["release-type"]
    version_file = config.get("version-file", "version.txt")
    files = set(subprocess.check_output(
        ["git", "diff", "--name-only", f"{base}...{head}"], text=True
    ).splitlines())
    allowed = {".release-please-manifest.json", "CHANGELOG.md"}
    if kind == "node":
        allowed.add("package.json")
    elif kind == "simple":
        allowed.add(version_file)
        if "README.md" in config.get("extra-files", []):
            allowed.add("README.md")
    elif kind != "go":
        return False
    if not files or not files <= allowed:
        return False
    before = json.loads(read(base, ".release-please-manifest.json"))
    after = json.loads(read(head, ".release-please-manifest.json"))
    if set(before) != {"."} or set(after) != {"."}:
        return False
    old, new = before["."], after["."]
    if not all(isinstance(v, str) and re.fullmatch(r"\d+\.\d+\.\d+", v) for v in (old, new)):
        return False
    if tuple(map(int, new.split("."))) <= tuple(map(int, old.split("."))):
        return False
    if kind == "node":
        packages = [json.loads(read(rev, "package.json")) for rev in (base, head)]
        if [p.pop("version", None) for p in packages] != [old, new] or packages[0] != packages[1]:
            return False
    if version_file in files and [read(rev, version_file).strip() for rev in (base, head)] != [old, new]:
        return False
    if "README.md" in files and read(base, "README.md").replace(old, "<version>") != read(head, "README.md").replace(new, "<version>"):
        return False
    return True


def main():
    run_full = True
    if (os.environ.get("GITHUB_EVENT_NAME") == "pull_request"
            and os.environ.get("HEAD_REF") == "release-please--branches--main"
            and os.environ.get("HEAD_REPO") == os.environ.get("GITHUB_REPOSITORY")
            and os.environ.get("HEAD_REPO")):
        run_full = not metadata_only(os.environ["BASE_SHA"], os.environ["HEAD_SHA"])
    output = f"run_full={str(run_full).lower()}\n"
    if os.environ.get("GITHUB_OUTPUT"):
        with Path(os.environ["GITHUB_OUTPUT"]).open("a") as stream:
            stream.write(output)
    print(output, end="")


if __name__ == "__main__":
    main()
