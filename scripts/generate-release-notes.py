#!/usr/bin/env python3

import argparse
import html
from pathlib import Path
import re
import subprocess


VERSION_PATTERN = r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
CONVENTIONAL_COMMIT = re.compile(r"^(?P<kind>[a-z][a-z0-9-]*)(?:\([^()]+\))?!?:\s*(?P<subject>.+)$")
RELEASE_ONLY = re.compile(
    rf"(?:(?:release|bump(?:\s+version)?|update version|version|发布|发版|更新版本|升级版本)(?:\s+to)?[\s:：]*)?v?{VERSION_PATTERN}",
    re.IGNORECASE,
)
CHANGE_GROUPS = (
    ("新功能", {"feat"}),
    ("问题修复", {"fix"}),
    ("体验优化与重构", {"perf", "refactor", "style"}),
    ("文档更新", {"docs"}),
    ("构建与维护", {"ci", "build", "test", "chore"}),
    ("其他变更", set()),
)
SING_BOX_COMMIT = "54ed58499d7063136ed52dabf87d179d252425d0"


def git(*arguments):
    return subprocess.run(["git", *arguments], check=True, capture_output=True, text=True, encoding="utf-8").stdout


def version_key(version):
    match = re.fullmatch(VERSION_PATTERN, version)
    if match is None:
        raise ValueError(f"无效的发行版本 {version!r}")
    major, minor, patch, prerelease = match.groups()
    identifiers = []
    if prerelease is not None:
        for part in prerelease.split("."):
            if part.isdigit():
                if len(part) > 1 and part.startswith("0"):
                    raise ValueError(f"无效的发行版本 {version!r}")
                identifiers.append((0, int(part)))
            else:
                identifiers.append((1, part))
    return int(major), int(minor), int(patch), prerelease is None, tuple(identifiers)


def previous_tag(version, commit):
    current = version_key(version)
    candidates = []
    for tag in git("tag", "--merged", commit, "--list", "v*").splitlines():
        try:
            key = version_key(tag[1:])
        except ValueError:
            continue
        if key < current:
            candidates.append((key, tag))
    for _, tag in sorted(candidates, reverse=True):
        if git("rev-parse", f"refs/tags/{tag}^{{commit}}").strip() != commit:
            return tag
    return None


def parse_commit(line):
    full_sha, short_sha, subject = line.split("\t", 2)
    match = CONVENTIONAL_COMMIT.fullmatch(subject)
    kind = match["kind"] if match else ""
    release_only = RELEASE_ONLY.fullmatch(match["subject"] if match else subject) is not None
    return full_sha, short_sha, subject, kind, release_only


def markdown_text(text):
    return re.sub(r"([\\`*_\[\]~])", r"\\\1", html.escape(text, quote=False))


def generate(version, repository, server_url, sha):
    major, minor, _, stable, _ = version_key(version)
    commit = git("rev-parse", "--verify", "--end-of-options", f"{sha}^{{commit}}").strip()
    tag = f"v{version}"
    previous = previous_tag(version, commit)
    revision = f"refs/tags/{previous}..{commit}" if previous else commit
    log_format = "--format=%H%x09%h%x09%s"
    history = git("log", "--no-merges", log_format, revision, "--").rstrip("\n")
    commits = [parse_commit(line) for line in history.split("\n")] if history else []
    changes = [entry for entry in commits if not entry[-1]]
    if not changes:
        changes = [parse_commit(git("log", "-1", log_format, commit, "--").rstrip("\n"))]

    project = f"{server_url.rstrip('/')}/{repository}"
    source = f"{project}/blob/{commit}"
    lines = ["## 更新内容"]
    groups = {title: [] for title, _ in CHANGE_GROUPS}
    for full_sha, short_sha, subject, kind, _ in changes:
        group = next((title for title, kinds in CHANGE_GROUPS if kind in kinds), "其他变更")
        groups[group].append(f"- {markdown_text(subject)} ([{short_sha}]({project}/commit/{full_sha}))")
    for title, entries in groups.items():
        if entries:
            lines.extend(["", f"### {title}", "", *entries])
    if previous:
        lines.extend(["", f"[完整对比 {previous} → {tag}]({project}/compare/{previous}...{tag})"])

    image = f"ghcr.io/{repository.lower()}:{version}"
    tags = [version, f"{major}.{minor}", str(major), "latest"] if stable else [version]
    tag_list = "、".join(f"`{item}`" for item in tags)
    lines.extend([
        "", "## 下载与运行", "",
        f"- [下载二进制及校验和]({project}/releases/tag/{tag})（Linux、FreeBSD、macOS，amd64 和 arm64）。",
        f"- 多架构镜像可用 `docker pull {image}` 拉取，本版标签为 {tag_list}。Compose 可通过 `GHCR_IMAGE={image}` 固定版本。",
        f"- 首次运行请阅读用户协议并在终端输入 `y`。Docker 或非交互式部署请在同意后设置 `ACCEPT_USER_AGREEMENT=true`。完整条款见 [USER_AGREEMENT.md]({source}/USER_AGREEMENT.md)。",
        "", "## 第三方许可证与源码", "",
        "随发行版提供的 sing-box 内核作为独立进程运行，按 GPL-3.0-or-later 分发。",
        f"[GPL v3 正文]({source}/licenses/SING-BOX-GPL-3.0.txt) · "
        f"[第三方声明与重编译说明]({source}/THIRD_PARTY_NOTICES.md) · "
        f"[sing-box 对应源码归档](https://github.com/SagerNet/sing-box/archive/{SING_BOX_COMMIT}.tar.gz)",
    ])
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser(description="根据 Git 历史生成 GitHub Release 说明")
    parser.add_argument("--version", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--server-url", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        notes = generate(args.version, args.repository, args.server_url, args.sha)
        args.output.write_text(notes, encoding="utf-8")
    except (ValueError, OSError) as error:
        parser.exit(1, f"{error}\n")
    except subprocess.CalledProcessError as error:
        parser.exit(1, error.stderr)


if __name__ == "__main__":
    main()
