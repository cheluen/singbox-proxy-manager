from datetime import datetime, timezone
import importlib.util
from pathlib import Path
import unittest


MODULE_PATH = Path(__file__).resolve().parent / "cleanup-ghcr-packages.py"
SPEC = importlib.util.spec_from_file_location("cleanup_ghcr_packages", MODULE_PATH)
CLEANUP = importlib.util.module_from_spec(SPEC)
assert SPEC is not None and SPEC.loader is not None
SPEC.loader.exec_module(CLEANUP)


def make_version(vid, digest, tags, created_at):
    return {
        "id": vid,
        "name": digest,
        "created_at": created_at,
        "metadata": {"container": {"tags": tags}},
    }


class CleanupGHCRPackagesTest(unittest.TestCase):
    def test_protects_versioned_and_referenced_children_while_pruning_legacy_sha_and_orphans(self):
        now = datetime(2026, 11, 24, 12, 0, 0, tzinfo=timezone.utc)
        versions = [
            make_version(1, "sha256:index-1728", ["1.7.28", "1.7", "1", "latest"], "2026-11-24T09:00:00Z"),
            make_version(2, "sha256:amd64-1728", [], "2026-11-24T09:00:00Z"),
            make_version(3, "sha256:sbom-1728", [], "2026-11-24T09:00:00Z"),
            make_version(4, "sha256:edge-current", ["edge"], "2026-11-24T08:00:00Z"),
            make_version(5, "sha256:legacy-sha-only", ["444ad3af2bbb6c0cbe23ac7153f219b93bbd00cb"], "2026-11-20T08:00:00Z"),
            make_version(6, "sha256:orphan-untagged", [], "2026-11-20T08:00:00Z"),
            make_version(7, "sha256:recent-untagged", [], "2026-11-24T11:45:00Z"),
            make_version(8, "sha256:old-release", ["v1.7.27", "v1.7.27-linux-amd64-arm64", "444ad3af2bbb6c0cbe23ac7153f219b93bbd00cb"], "2026-11-20T08:00:00Z"),
        ]
        kept = CLEANUP.select_kept_root_digests(versions, now=now, min_age_seconds=3600)
        self.assertEqual(kept, ["sha256:index-1728", "sha256:edge-current", "sha256:old-release"])
        referenced = set(kept) | {"sha256:amd64-1728", "sha256:sbom-1728"}

        sha_del, untagged_del = CLEANUP.plan_deletions(versions, referenced, now=now, min_age_seconds=3600)
        self.assertEqual([v["id"] for v in sha_del], [5])
        self.assertEqual([v["id"] for v in untagged_del], [6])

    def test_recent_sha_index_is_included_in_kept_roots_so_older_children_stay_protected(self):
        now = datetime(2026, 11, 24, 12, 0, 0, tzinfo=timezone.utc)
        versions = [
            make_version(10, "sha256:recent-sha-index", ["a" * 40], "2026-11-24T11:50:00Z"),
            make_version(11, "sha256:older-shared-child", [], "2026-11-20T08:00:00Z"),
        ]
        kept = CLEANUP.select_kept_root_digests(versions, now=now, min_age_seconds=3600)
        self.assertIn("sha256:recent-sha-index", kept)
        referenced = set(kept) | {"sha256:older-shared-child"}
        sha_del, untagged_del = CLEANUP.plan_deletions(versions, referenced, now=now, min_age_seconds=3600)
        self.assertEqual(sha_del, [])
        self.assertEqual(untagged_del, [])

    def test_fail_safe_aborts_all_deletions_when_reference_graph_incomplete(self):
        now = datetime(2026, 11, 24, 12, 0, 0, tzinfo=timezone.utc)
        versions = [
            make_version(5, "sha256:legacy-sha-only", ["444ad3af2bbb6c0cbe23ac7153f219b93bbd00cb"], "2026-11-20T08:00:00Z"),
            make_version(6, "sha256:orphan-untagged", [], "2026-11-20T08:00:00Z"),
        ]
        sha_del, untagged_del = CLEANUP.plan_deletions(
            versions, set(), now=now, min_age_seconds=3600, reference_graph_complete=False
        )
        self.assertEqual(sha_del, [])
        self.assertEqual(untagged_del, [])


if __name__ == "__main__":
    unittest.main()
