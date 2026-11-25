import importlib.util
import os
from pathlib import Path
import subprocess
import sys
from urllib.error import HTTPError


def load_script(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


notes = load_script("generate-release-notes")
ghcr = load_script("cleanup-ghcr-packages")


def rolling_tags(version, tags):
    major, minor, patch, stable, _ = notes.version_key(version)
    if not stable:
        return []
    newer = []
    for tag in tags:
        try:
            key = notes.version_key(tag.removeprefix("v"))
        except ValueError:
            continue
        if key[3] and key[:3] > (major, minor, patch):
            newer.append(key[:3])
    aliases = []
    if all(item[:2] != (major, minor) for item in newer):
        aliases.append(f"{major}.{minor}")
    if all(item[0] != major for item in newer):
        aliases.append(str(major))
    if not newer:
        aliases.append("latest")
    return aliases


def version_image_matches(repository, version, commit):
    token = ghcr.fetch_ghcr_bearer_token(repository, os.environ["GH_TOKEN"])
    try:
        manifest = ghcr.fetch_manifest(repository.lower(), version, token)
    except HTTPError as error:
        if error.code == 404:
            return True
        raise
    revision = manifest.get("annotations", {}).get("org.opencontainers.image.revision")
    if not revision:
        image = f"ghcr.io/{repository.lower()}:{version}"
        subprocess.run(["docker", "pull", "--quiet", "--platform", "linux/amd64", image], check=True, stdout=subprocess.DEVNULL)
        revision = subprocess.check_output(["docker", "image", "inspect", "--format",
                                            '{{index .Config.Labels "org.opencontainers.image.revision"}}', image], text=True).strip()
    return revision == commit


def publication(version, tags, release, repository, commit, check_image=False):
    tag = f"v{version}"
    allowed = True
    if release and tag in tags:
        owner = subprocess.check_output(["git", "rev-parse", "--verify", f"refs/tags/{tag}^{{commit}}"], text=True).strip()
        allowed = owner == commit
    if release and allowed and check_image:
        allowed = version_image_matches(repository, version, commit)
    image = "ghcr.io/" + repository.lower()
    aliases = rolling_tags(version, tags) if release and allowed else []
    return {
        "should_publish": str(allowed).lower(),
        "revision": commit,
        "image": image,
        "build_tag": f"{image}:{version if release else 'edge'}",
        "make_latest": str("latest" in aliases).lower(),
        "rolling": " ".join(aliases),
    }


if __name__ == "__main__":
    tags = subprocess.check_output(["git", "tag", "--list", "v*"], text=True).splitlines()
    commit = subprocess.check_output(["git", "rev-parse", "--verify", os.environ["GITHUB_SHA"] + "^{commit}"], text=True).strip()
    plan = publication(os.environ["APP_VERSION"], tags, os.environ["SHOULD_RELEASE"] == "true",
                       os.environ["GITHUB_REPOSITORY"], commit, "--check-image" in sys.argv[1:])
    if plan["should_publish"] == "false":
        print("::notice::版本已有其他提交的发行产物，跳过本次发布", file=sys.stderr)
    for key, value in plan.items():
        print(f"{key}={value}")
