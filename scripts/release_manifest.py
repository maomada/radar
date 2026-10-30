#!/usr/bin/env python3
"""Create and update the non-secret release evidence manifest."""

import argparse
import hashlib
import json
import re
import sys
from pathlib import Path


IMAGE_NAMES = ("radar", "radar-portal")
CHART_NAMES = ("radar", "radar-portal")


def _sha256(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def _chart_checksums(path):
    checksums = {}
    for line in Path(path).read_text().splitlines():
        digest, filename = line.split(None, 1)
        checksums[filename.strip()] = digest
    return checksums


def create_manifest(
    artifact_root,
    source_sha,
    version,
    actor,
    run_id,
    run_url,
    ci_url,
    codeql_url,
    publish,
):
    root = Path(artifact_root)
    images = {}
    for name in IMAGE_NAMES:
        directory = root / "images" / f"release-image-{name}"
        archive = directory / f"{name}.oci.tar"
        checksum_file = directory / f"{name}.oci.tar.sha256"
        metadata = json.loads((directory / f"{name}.metadata.json").read_text())
        if metadata.get("sourceSha") != source_sha or metadata.get("version") != version:
            raise ValueError(f"Image artifact metadata does not match the selected source: {name}")
        digest = _sha256(archive)
        if checksum_file.read_text().split()[0] != digest:
            raise ValueError(f"Image archive checksum does not match: {name}")
        images[name] = {
            "reference": f"ghcr.io/maomada/{name}:{version}",
            "archiveSha256": digest,
            "buildDigest": metadata.get("buildDigest") or None,
            "publication": "not-attempted",
            "publishedDigest": None,
        }

    chart_dir = root / "charts"
    checksums = _chart_checksums(chart_dir / "SHA256SUMS")
    charts = {}
    for name in CHART_NAMES:
        filename = f"{name}-{version}.tgz"
        archive = chart_dir / filename
        digest = _sha256(archive)
        if checksums.get(filename) != digest:
            raise ValueError(f"Chart archive checksum does not match: {filename}")
        charts[name] = {
            "reference": f"ghcr.io/maomada/charts/{name}:{version}",
            "archiveSha256": digest,
            "publication": "not-attempted",
            "publishedDigest": None,
        }

    return {
        "schemaVersion": 1,
        "repository": "maomada/radar",
        "sourceSha": source_sha,
        "version": version,
        "checks": {"ci": ci_url, "codeql": codeql_url},
        "actor": actor,
        "run": {"id": run_id, "url": run_url},
        "images": images,
        "charts": charts,
        "publication": {
            "requested": publish,
            "status": "pending" if publish else "dry-run",
            "preflight": "not-run",
            "jobResult": "not-started",
        },
    }


def write_manifest(path, manifest):
    Path(path).write_text(json.dumps(manifest, indent=2, sort_keys=True) + "\n")


def record_target(manifest_path, kind, name, status, digest=None):
    manifest = json.loads(Path(manifest_path).read_text())
    collection = manifest[kind + "s"]
    if name not in collection:
        raise ValueError("Unknown release target")
    if status not in ("attempted", "push-failed-or-unknown", "published"):
        raise ValueError("Invalid target status")
    if status == "published":
        if digest is None or not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
            raise ValueError("A valid SHA-256 digest is required for a published target")
    elif digest is not None:
        raise ValueError("A digest is only valid for a published target")
    collection[name]["publication"] = status
    collection[name]["publishedDigest"] = digest
    write_manifest(manifest_path, manifest)


def set_preflight(manifest_path, status):
    if status not in ("passed", "blocked"):
        raise ValueError("Invalid preflight status")
    manifest = json.loads(Path(manifest_path).read_text())
    manifest["publication"]["preflight"] = status
    write_manifest(manifest_path, manifest)


def finalize_manifest(manifest_path, job_result, validation_outcome, preflight_outcome):
    manifest = json.loads(Path(manifest_path).read_text())
    publication = manifest["publication"]
    publication["jobResult"] = job_result
    if not publication["requested"]:
        publication["status"] = "dry-run"
    elif validation_outcome == "failure":
        publication["status"] = "blocked-validation"
    elif preflight_outcome == "failure":
        publication["status"] = "blocked-preflight"
        publication["preflight"] = "blocked"
    else:
        targets = [
            *manifest["images"].values(),
            *manifest["charts"].values(),
        ]
        published = sum(item["publication"] == "published" for item in targets)
        if job_result == "success" and published == len(targets):
            publication["status"] = "published"
        elif published or any(
            item["publication"] in ("attempted", "push-failed-or-unknown")
            for item in targets
        ):
            publication["status"] = "partial-or-unknown"
        elif publication["preflight"] == "passed":
            publication["status"] = "failed-after-preflight"
        else:
            publication["status"] = "failed-before-publish"
    write_manifest(manifest_path, manifest)
    return manifest


def summary(manifest):
    lines = [
        f"## GHCR release {manifest['version']}",
        "",
        f"- Source: `{manifest['sourceSha']}`",
        f"- Actor: `{manifest['actor']}`",
        f"- Run: [{manifest['run']['id']}]({manifest['run']['url']})",
        f"- CI evidence: [{manifest['checks']['ci']}]({manifest['checks']['ci']})",
        f"- CodeQL evidence: [{manifest['checks']['codeql']}]({manifest['checks']['codeql']})",
        f"- Publication status: **{manifest['publication']['status']}**",
        "",
        "### Images",
    ]
    for name, image in manifest["images"].items():
        detail = f"archive SHA-256 `{image['archiveSha256']}`"
        if image["buildDigest"]:
            detail += f", build digest `{image['buildDigest']}`"
        if image["publishedDigest"]:
            detail += f", published digest `{image['publishedDigest']}`"
        lines.append(f"- `{image['reference']}` — {image['publication']}; {detail}")
    lines.extend(["", "### Charts"])
    for name, chart in manifest["charts"].items():
        detail = f"archive SHA-256 `{chart['archiveSha256']}`"
        if chart["publishedDigest"]:
            detail += f", published digest `{chart['publishedDigest']}`"
        lines.append(f"- `{chart['reference']}` — {chart['publication']}; {detail}")
    if not manifest["publication"]["requested"]:
        lines.extend(
            [
                "",
                "> Dry run proves only that images built and charts packaged. It does not prove registry permissions, collision checks, or a successful push.",
            ]
        )
    elif manifest["publication"]["status"] != "published":
        lines.extend(
            [
                "",
                "> Publication may be partial or unknown. Do not retry or overwrite these refs; reconcile registry state and choose a fresh version.",
            ]
        )
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)
    create = subparsers.add_parser("create")
    for argument in (
        "artifact-root",
        "source-sha",
        "version",
        "actor",
        "run-id",
        "run-url",
        "ci-url",
        "codeql-url",
        "output",
    ):
        create.add_argument(f"--{argument}", required=True)
    create.add_argument("--publish", action="store_true")

    target = subparsers.add_parser("target")
    target.add_argument("--manifest", required=True)
    target.add_argument("--kind", choices=("image", "chart"), required=True)
    target.add_argument("--name", required=True)
    target.add_argument(
        "--status",
        choices=("attempted", "push-failed-or-unknown", "published"),
        required=True,
    )
    target.add_argument("--digest")

    preflight = subparsers.add_parser("preflight")
    preflight.add_argument("--manifest", required=True)
    preflight.add_argument("--status", choices=("passed", "blocked"), required=True)

    finalize = subparsers.add_parser("finalize")
    finalize.add_argument("--manifest", required=True)
    finalize.add_argument("--job-result", required=True)
    finalize.add_argument("--validation-outcome", required=True)
    finalize.add_argument("--preflight-outcome", required=True)

    show = subparsers.add_parser("summary")
    show.add_argument("--manifest", required=True)
    args = parser.parse_args()

    try:
        if args.command == "create":
            manifest = create_manifest(
                args.artifact_root,
                args.source_sha,
                args.version,
                args.actor,
                args.run_id,
                args.run_url,
                args.ci_url,
                args.codeql_url,
                args.publish,
            )
            write_manifest(args.output, manifest)
        elif args.command == "target":
            record_target(args.manifest, args.kind, args.name, args.status, args.digest)
        elif args.command == "preflight":
            set_preflight(args.manifest, args.status)
        elif args.command == "finalize":
            finalize_manifest(
                args.manifest,
                args.job_result,
                args.validation_outcome,
                args.preflight_outcome,
            )
        else:
            manifest = json.loads(Path(args.manifest).read_text())
            print(summary(manifest), end="")
    except (OSError, ValueError, KeyError) as error:
        print(f"Release manifest error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
