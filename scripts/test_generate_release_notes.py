import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("generate-release-notes.py").resolve()


class ReleaseNotesTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.git("init", "--initial-branch=main")
        self.git("config", "user.name", "Release Notes Test")
        self.git("config", "user.email", "release-test@example.invalid")

    def git(self, *arguments):
        return subprocess.run(
            ["git", *arguments], cwd=self.root, check=True, capture_output=True, text=True,
            env={**os.environ, "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.devnull},
        ).stdout.strip()

    def commit(self, message):
        self.git("commit", "--allow-empty", "-m", message)
        return self.git("rev-parse", "HEAD")

    def notes(self, version):
        output = self.root / "notes.md"
        subprocess.run(
            [sys.executable, str(SCRIPT), "--version", version, "--repository", "Owner/Repo",
             "--server-url", "https://git.example.invalid/", "--sha", self.git("rev-parse", "HEAD"),
             "--output", str(output)],
            cwd=self.root, check=True, capture_output=True, text=True,
        )
        return output.read_text(encoding="utf-8")

    def test_groups_reachable_previous_and_same_tag_rerun(self):
        self.commit("feat: 前一版内容")
        self.git("tag", "-a", "v1.7.26", "-m", "previous")
        self.git("switch", "-c", "unmerged")
        self.commit("fix: 未合并提交")
        self.git("tag", "v1.7.27")
        self.git("switch", "main")
        self.git("switch", "-c", "merged")
        feature = self.commit("feat(panel)!: 保存列宽")
        self.git("switch", "main")
        self.commit("fix: 修复启动")
        self.git("merge", "--no-ff", "merged", "-m", "应跳过的合并提交")
        self.git("tag", "v2.0.0")
        self.git("tag", "v1.7-preview")
        self.commit("perf: 减少等待")
        self.commit("refactor: 整理设置")
        self.commit("style: 调整样式")
        self.commit("docs: 补充使用说明")
        self.commit("ci: 发布镜像")
        self.commit("build: 校验打包")
        self.commit("test: 覆盖迁移")
        self.commit("chore: 更新依赖")
        self.commit("普通提交 [方括号] <b>")
        self.commit("chore(release): 发布 v1.7.28")
        self.git("tag", "v1.7.28")
        self.git("tag", "v1.7.28-rc.1")

        notes = self.notes("1.7.28")
        self.assertIn("/compare/v1.7.26...v1.7.28", notes)
        self.assertIn(f"(https://git.example.invalid/Owner/Repo/commit/{feature})", notes)
        self.assertIn(r"普通提交 \[方括号\] &lt;b&gt;", notes)
        self.assertEqual(
            [line for line in notes.splitlines() if line.startswith("### ")],
            ["### 新功能", "### 问题修复", "### 体验优化与重构", "### 文档更新", "### 构建与维护", "### 其他变更"],
        )
        for text in ("前一版内容", "未合并提交", "应跳过的合并提交", "chore(release): 发布", "CVE-"):
            self.assertNotIn(text, notes)
        self.assertIn("`1.7.28`、`1.7`、`1`、`latest`", notes)
        self.assertIn("docker pull ghcr.io/owner/repo:1.7.28", notes)

    def test_prerelease_version_order_and_artifacts(self):
        self.commit("chore: v1.7.28")
        self.git("tag", "v1.7.28")
        self.commit("feat: 第一批预览功能")
        self.git("tag", "v1.8.0-beta.9")
        self.commit("fix: 上一版预览修复")
        self.git("tag", "v1.8.0-beta.10")
        self.commit("fix: 本次预览修复")

        notes = self.notes("1.8.0-beta.11")
        self.assertIn("/compare/v1.8.0-beta.10...v1.8.0-beta.11", notes)
        self.assertIn("本次预览修复", notes)
        self.assertNotIn("第一批预览功能", notes)
        self.assertNotIn("### 新功能", notes)
        self.assertIn("本版标签为 `1.8.0-beta.11`。", notes)
        self.assertNotIn("`latest`", notes)
        for text in ("ACCEPT_USER_AGREEMENT=true", "USER_AGREEMENT.md", "SING-BOX-GPL-3.0.txt",
                     "THIRD_PARTY_NOTICES.md", "github.com/SagerNet/sing-box/archive/"):
            self.assertIn(text, notes)

    def test_first_release_and_version_only_fallback(self):
        sha = self.commit("chore(release): bump version to v1.0.0")
        notes = self.notes("1.0.0")
        self.assertNotIn("/compare/", notes)
        self.assertIn("chore(release): bump version to v1.0.0", notes)
        self.assertIn(f"/commit/{sha}", notes)
        self.assertIn("### 构建与维护", notes)
        self.assertNotIn("### 其他变更", notes)

    def test_empty_nonmerge_range_falls_back_to_head(self):
        ancestor = self.commit("feat: 更早的提交")
        previous = self.commit("fix: 前一版内容")
        self.git("tag", "v1.0.0")
        head = self.git("commit-tree", "HEAD^{tree}", "-p", previous, "-p", ancestor, "-m", "合并发行提交")
        self.git("update-ref", "refs/heads/main", head)

        notes = self.notes("1.0.1")
        self.assertIn("/compare/v1.0.0...v1.0.1", notes)
        self.assertIn(f"/commit/{head}", notes)
        self.assertIn("合并发行提交", notes)
        self.assertNotIn("更早的提交", notes)
        self.assertNotIn("前一版内容", notes)


if __name__ == "__main__":
    unittest.main()
