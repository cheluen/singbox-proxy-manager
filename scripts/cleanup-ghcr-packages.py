#!/usr/bin/env python3
"""安全清理 GHCR 历史纯 40 位 SHA 镜像与无任何保留索引引用的孤立未打标层。"""

import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timedelta, timezone
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request


FULL_SHA_TAG = re.compile(r"^[0-9a-f]{40}$")
INDEX_MEDIA_TYPES = {
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
}
MANIFEST_ACCEPT = ", ".join((
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
    "application/vnd.oci.image.manifest.v1+json",
    "application/vnd.docker.distribution.manifest.v2+json",
))


def parse_timestamp(raw):
    return datetime.fromisoformat(raw.replace("Z", "+00:00"))


def is_legacy_sha_only(tags):
    return bool(tags) and all(FULL_SHA_TAG.fullmatch(tag) is not None for tag in tags)


def select_kept_root_digests(versions, *, now, min_age_seconds=3600):
    """返回所有将会被保留的带标签镜像 digest（含非纯 SHA 版本与未满宽限期的纯 SHA 版本）。"""
    cutoff = now - timedelta(seconds=max(0, min_age_seconds))
    kept = []
    for version in versions:
        tags = version.get("metadata", {}).get("container", {}).get("tags") or []
        digest = version.get("name") or ""
        if not tags or not digest:
            continue
        created = parse_timestamp(version["created_at"])
        if not is_legacy_sha_only(tags) or created > cutoff:
            kept.append(digest)
    return list(dict.fromkeys(kept))


def plan_deletions(versions, referenced_digests, *, now, min_age_seconds=3600, reference_graph_complete=True):
    """根据当前版本列表与完整解析后的引用图计算可安全删除的版本。"""
    if not reference_graph_complete:
        return [], []

    cutoff = now - timedelta(seconds=max(0, min_age_seconds))
    protected = set(referenced_digests)
    protected.update(select_kept_root_digests(versions, now=now, min_age_seconds=min_age_seconds))

    sha_to_delete = []
    untagged_to_delete = []

    for version in versions:
        tags = version.get("metadata", {}).get("container", {}).get("tags") or []
        digest = version.get("name") or ""
        created = parse_timestamp(version["created_at"])
        if created > cutoff:
            continue
        if is_legacy_sha_only(tags):
            if digest not in protected:
                sha_to_delete.append(version)
        elif not tags:
            if digest and digest not in protected:
                untagged_to_delete.append(version)

    return sha_to_delete, untagged_to_delete


def api_request(url, token, *, method="GET", accept="application/vnd.github+json"):
    req = urllib.request.Request(
        url,
        method=method,
        headers={
            "Authorization": f"Bearer {token}",
            "Accept": accept,
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "sbpm-ghcr-cleanup",
        },
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        body = resp.read()
        return resp.status, json.loads(body.decode("utf-8")) if body else None


def list_package_versions(owner_type, owner, package, token):
    encoded = urllib.parse.quote(package, safe="")
    prefix = f"https://api.github.com/{owner_type}/{owner}/packages/container/{encoded}/versions"
    versions = []
    page = 1
    while True:
        url = f"{prefix}?per_page=100&page={page}"
        _, batch = api_request(url, token)
        if not batch:
            break
        versions.extend(batch)
        if len(batch) < 100:
            break
        page += 1
    return versions


def fetch_ghcr_bearer_token(repository, github_token):
    scope = f"repository:{repository.lower()}:pull"
    url = f"https://ghcr.io/token?service=ghcr.io&scope={urllib.parse.quote(scope, safe=':/')}"
    basic = base64.b64encode(f"x-access-token:{github_token}".encode("utf-8")).decode("ascii")
    req = urllib.request.Request(
        url,
        headers={
            "Authorization": f"Basic {basic}",
            "User-Agent": "sbpm-ghcr-cleanup",
        },
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        data = json.loads(resp.read().decode("utf-8"))
        token = data.get("token")
        if not token:
            raise RuntimeError("GHCR token response missing token field")
        return token


def fetch_manifest(repo_lower, digest, registry_token):
    url = f"https://ghcr.io/v2/{repo_lower}/manifests/{digest}"
    req = urllib.request.Request(
        url,
        headers={
            "Authorization": f"Bearer {registry_token}",
            "Accept": MANIFEST_ACCEPT,
            "User-Agent": "sbpm-ghcr-cleanup",
        },
    )
    with urllib.request.urlopen(req, timeout=15) as resp:
        return json.loads(resp.read().decode("utf-8"))


def collect_referenced_digests(repository, root_digests, registry_token):
    """并发读取所有保留镜像的 OCI Index/Manifest List，收集其引用的全部子 digest。"""
    referenced = set(root_digests)
    pending = list(dict.fromkeys(d for d in root_digests if d))
    seen = set()
    repo_lower = repository.lower()

    with ThreadPoolExecutor(max_workers=8) as pool:
        while pending:
            batch = [d for d in pending if d not in seen]
            if not batch:
                break
            seen.update(batch)
            pending = []
            payloads = list(pool.map(lambda d: fetch_manifest(repo_lower, d, registry_token), batch))
            for payload in payloads:
                for entry in payload.get("manifests") or []:
                    child = entry.get("digest")
                    if not child:
                        continue
                    referenced.add(child)
                    if entry.get("mediaType") in INDEX_MEDIA_TYPES and child not in seen:
                        pending.append(child)
                subject = (payload.get("subject") or {}).get("digest")
                if subject:
                    referenced.add(subject)

    return referenced


def delete_package_version(owner_type, owner, package, version_id, token):
    encoded = urllib.parse.quote(package, safe="")
    url = f"https://api.github.com/{owner_type}/{owner}/packages/container/{encoded}/versions/{version_id}"
    api_request(url, token, method="DELETE")


def main():
    parser = argparse.ArgumentParser(description="清理 GHCR 历史纯 SHA 镜像与孤立未打标层")
    parser.add_argument("--repository", required=True, help="owner/repo")
    parser.add_argument("--package", required=True, help="GHCR package name")
    parser.add_argument("--owner-type", choices=("users", "orgs"), default="users")
    parser.add_argument("--min-age-seconds", type=int, default=3600)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args()

    token = os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN") or ""
    if not token:
        print("缺少 GH_TOKEN/GITHUB_TOKEN，跳过 GHCR 清理", file=sys.stderr)
        return 1

    now = datetime.now(timezone.utc)
    owner = args.repository.split("/", 1)[0]
    versions = list_package_versions(args.owner_type, owner, args.package, token)
    kept_roots = select_kept_root_digests(versions, now=now, min_age_seconds=args.min_age_seconds)

    referenced = set(kept_roots)
    reference_graph_complete = True
    try:
        registry_token = fetch_ghcr_bearer_token(args.repository, token)
        referenced = collect_referenced_digests(args.repository, kept_roots, registry_token)
    except Exception as error:
        # 只要有任何保留镜像的引用图解析失败，直接中止本次全部删除，确保零误删。
        print(f"::warning::无法完整解析保留镜像引用图谱（{error}），中止本次 GHCR 清理以防误删", file=sys.stderr)
        reference_graph_complete = False

    sha_to_delete, untagged_to_delete = plan_deletions(
        versions,
        referenced,
        now=now,
        min_age_seconds=args.min_age_seconds,
        reference_graph_complete=reference_graph_complete,
    )

    targets = sha_to_delete + untagged_to_delete
    print(
        f"GHCR 版本总数: {len(versions)}，保留索引: {len(kept_roots)}，"
        f"受保护 digest: {len(referenced)}，待删纯 SHA: {len(sha_to_delete)}，"
        f"待删孤立未打标层: {len(untagged_to_delete)}"
    )

    if args.dry_run:
        for item in targets:
            print(f"[dry-run] delete id={item['id']} name={item.get('name')} tags={item.get('metadata', {}).get('container', {}).get('tags', [])}")
        return 0

    deleted = 0
    for item in targets:
        try:
            delete_package_version(args.owner_type, owner, args.package, item["id"], token)
            deleted += 1
        except urllib.error.HTTPError as error:
            if error.code == 404:
                continue
            print(f"::warning::删除版本 {item['id']} 失败（HTTP {error.code}）", file=sys.stderr)
            if error.code in (401, 403):
                break
    print(f"本次成功清理 GHCR 冗余版本数: {deleted}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
