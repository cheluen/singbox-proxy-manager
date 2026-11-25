import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError


spec = importlib.util.spec_from_file_location("publish_tags", Path(__file__).with_name("publish_tags.py"))
publish = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publish)


class PublishTagsTest(unittest.TestCase):
    def test_edge_and_release_outputs(self):
        for release, version, expected_tag, latest in [
            (False, "2.0.0", "edge", False),
            (True, "2.0.0-rc.1", "2.0.0-rc.1", False),
            (True, "1.2.2", "1.2.2", False),
            (True, "2.0.0", "2.0.0", True),
        ]:
            with self.subTest(release=release, version=version):
                plan = publish.publication(version, ["v1.2.3"], release, "Owner/Repo", "candidate")
                self.assertEqual(plan["should_publish"], "true")
                self.assertEqual(plan["build_tag"], "ghcr.io/owner/repo:" + expected_tag)
                self.assertEqual(plan["make_latest"], str(latest).lower())
                self.assertEqual("latest" in plan["rolling"].split(), latest)

    def test_existing_tag_requires_its_original_commit(self):
        with tempfile.TemporaryDirectory() as temporary:
            def git(*args):
                return subprocess.check_output(["git", "-C", temporary, "-c", "user.name=Release Test", "-c",
                                                "user.email=release@example.invalid", *args], text=True).strip()

            git("init", "-q", "--initial-branch=main")
            git("commit", "--allow-empty", "-qm", "first")
            owner = git("rev-parse", "HEAD")
            git("tag", "-a", "v1.2.3", "-m", "official")
            git("commit", "--allow-empty", "-qm", "same version at different commit")
            for commit, expected in [(owner, "true"), (git("rev-parse", "v1.2.3"), "true"), (git("rev-parse", "HEAD"), "false")]:
                output = subprocess.check_output([sys.executable, publish.__file__], cwd=temporary, text=True,
                                                 env={**os.environ, "GITHUB_REPOSITORY": "Owner/Repo", "GITHUB_SHA": commit,
                                                      "APP_VERSION": "1.2.3", "SHOULD_RELEASE": "true"})
                plan = dict(line.split("=", 1) for line in output.splitlines())
                self.assertEqual(plan["should_publish"], expected)
                self.assertEqual(plan["make_latest"], expected)

    def test_image_pushed_before_git_release_keeps_its_owner(self):
        with patch.dict(os.environ, GH_TOKEN="fixture"), patch.object(publish.ghcr, "fetch_ghcr_bearer_token"), \
             patch.object(publish.ghcr, "fetch_manifest") as manifest:
            manifest.return_value = {"annotations": {"org.opencontainers.image.revision": "first"}}
            for commit, expected in [("first", "true"), ("second", "false")]:
                self.assertEqual(publish.publication("1.2.3", [], True, "Owner/Repo", commit, True)["should_publish"], expected)
            manifest.return_value = {}
            with patch.object(publish.subprocess, "run"), patch.object(publish.subprocess, "check_output", return_value="first\n"):
                self.assertTrue(publish.version_image_matches("Owner/Repo", "1.2.3", "first"))
                self.assertFalse(publish.version_image_matches("Owner/Repo", "1.2.3", "second"))
            manifest.side_effect = HTTPError("", 404, "absent", {}, None)
            self.assertEqual(publish.publication("1.2.3", [], True, "Owner/Repo", "new", True)["should_publish"], "true")
            manifest.side_effect = HTTPError("", 403, "denied", {}, None)
            with self.assertRaises(HTTPError):
                publish.publication("1.2.3", [], True, "Owner/Repo", "new", True)

    def test_matching_git_tag_still_requires_matching_image_revision(self):
        with patch.dict(os.environ, GH_TOKEN="fixture"), patch.object(publish.ghcr, "fetch_ghcr_bearer_token"), \
             patch.object(publish.subprocess, "check_output", return_value="second\n"), \
             patch.object(publish.ghcr, "fetch_manifest") as manifest:
            for revision, expected, rolling in [("first", "false", ""), ("second", "true", "1.2 1 latest")]:
                with self.subTest(image_revision=revision):
                    manifest.return_value = {"annotations": {"org.opencontainers.image.revision": revision}}
                    plan = publish.publication("1.2.3", ["v1.2.3"], True, "Owner/Repo", "second", check_image=True)
                    self.assertEqual(plan["should_publish"], expected)
                    self.assertEqual(plan["make_latest"], expected)
                    self.assertEqual(plan["rolling"], rolling)

    def test_later_finishing_release_only_moves_current_aliases(self):
        for tags, expected in [
            (["v1.2.3", "v2.0.0-beta.1", "invalid"], ["1.2", "1", "latest"]),
            (["v1.2.4"], []),
            (["v1.3.0"], ["1.2"]),
            (["v2.0.0"], ["1.2", "1"]),
        ]:
            with self.subTest(tags=tags):
                self.assertEqual(publish.rolling_tags("1.2.3", tags), expected)
        self.assertEqual(publish.rolling_tags("2.0.0-beta.1", []), [])
