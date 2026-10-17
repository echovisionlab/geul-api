import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("release_scope", Path(__file__).with_name("release-scope.py"))
scope = importlib.util.module_from_spec(spec)
spec.loader.exec_module(scope)


class ReleaseScopeTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.previous = Path.cwd()
        os.chdir(self.directory.name)
        self.git("init", "-q")
        self.git("config", "user.name", "CI fixture")
        self.git("config", "user.email", "ci@example.invalid")

    def tearDown(self):
        os.chdir(self.previous)
        self.directory.cleanup()

    def git(self, *args):
        return subprocess.check_output(["git", *args], text=True).strip()

    def write(self, path, value):
        Path(path).write_text(json.dumps(value) if isinstance(value, dict) else value)

    def commit(self):
        self.git("add", ".")
        self.git("commit", "-qm", "fixture")
        return self.git("rev-parse", "HEAD")

    def base(self, kind):
        self.write("release-please-config.json", {"packages": {".": {
            "release-type": kind, "version-file": "VERSION", "extra-files": ["README.md"]
        }}})
        self.write(".release-please-manifest.json", {".": "0.1.0"})
        self.write("package.json", {"version": "0.1.0", "dependencies": {"example": "1.0.0"}})
        self.write("VERSION", "0.1.0\n")
        self.write("README.md", "Current snapshot: `0.1.0` <!-- x-release-please-version -->\n")
        self.write("CHANGELOG.md", "# Changelog\n")
        return self.commit()

    def release(self, kind):
        self.write(".release-please-manifest.json", {".": "0.1.1"})
        self.write("CHANGELOG.md", "# Changelog\n\n## 0.1.1\n")
        if kind == "node":
            self.write("package.json", {"version": "0.1.1", "dependencies": {"example": "1.0.0"}})
        elif kind == "simple":
            self.write("VERSION", "0.1.1\n")
            self.write("README.md", "Current snapshot: `0.1.1` <!-- x-release-please-version -->\n")

    def test_release_metadata_skips_application_checks(self):
        for kind in ["go", "node", "simple"]:
            with self.subTest(kind=kind):
                base = self.base(kind)
                self.release(kind)
                self.assertTrue(scope.metadata_only(base, self.commit()))

    def test_source_change_requires_application_checks(self):
        base = self.base("go")
        self.release("go")
        self.write("source.go", "package example\n")
        self.assertFalse(scope.metadata_only(base, self.commit()))

    def test_dependency_change_requires_application_checks(self):
        base = self.base("node")
        self.release("node")
        self.write("package.json", {"version": "0.1.1", "dependencies": {"example": "2.0.0"}})
        self.assertFalse(scope.metadata_only(base, self.commit()))

    def test_substantive_readme_change_requires_application_checks(self):
        base = self.base("simple")
        self.release("simple")
        self.write("README.md", "Changed instructions for 0.1.1\n")
        self.assertFalse(scope.metadata_only(base, self.commit()))

    def test_inconsistent_package_version_requires_application_checks(self):
        base = self.base("node")
        self.release("node")
        self.write(".release-please-manifest.json", {".": "0.1.2"})
        self.assertFalse(scope.metadata_only(base, self.commit()))

    def test_normal_and_fork_pull_requests_keep_application_checks(self):
        for branch, repository in [("feature", "org/repo"), ("release-please--branches--main", "fork/repo")]:
            with self.subTest(branch=branch, repository=repository):
                output = io.StringIO()
                with patch.dict(os.environ, {"GITHUB_EVENT_NAME": "pull_request", "HEAD_REF": branch,
                        "HEAD_REPO": repository, "GITHUB_REPOSITORY": "org/repo", "GITHUB_OUTPUT": ""}):
                    with contextlib.redirect_stdout(output):
                        scope.main()
                self.assertEqual(output.getvalue(), "run_full=true\n")
