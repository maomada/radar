#!/usr/bin/env python3
"""Stage and package version-aligned owned Radar OCI charts."""

import argparse
import hashlib
import json
import re
import shutil
import subprocess
import sys
from pathlib import Path

from release_policy import REPOSITORY, validate_inputs


CHARTS = {
    "radar": "ghcr.io/maomada/radar",
    "radar-portal": "ghcr.io/maomada/radar-portal",
}


def _replace_top_level_scalar(content, key, value):
    replacement = f"{key}: {json.dumps(value)}"
    updated, count = re.subn(
        rf"(?m)^{re.escape(key)}:\s*.*$",
        lambda _: replacement,
        content,
    )
    if count != 1:
        raise ValueError(f"Expected one top-level {key} field")
    return updated


def _replace_annotation(content, key, value):
    lines = content.splitlines()
    annotation_index = next(
        (index for index, line in enumerate(lines) if line == "annotations:"),
        None,
    )
    if annotation_index is None:
        lines.extend(
            [
                "annotations:",
                f"  {key}: {json.dumps(value)}",
            ]
        )
        return "\n".join(lines) + "\n"
    for index in range(annotation_index + 1, len(lines)):
        line = lines[index]
        if line and not line[0].isspace():
            break
        if re.match(rf"^  {re.escape(key)}\s*:", line):
            lines[index] = f"  {key}: {json.dumps(value)}"
            return "\n".join(lines) + "\n"
    lines.insert(annotation_index + 1, f"  {key}: {json.dumps(value)}")
    return "\n".join(lines) + "\n"


def _replace_image_values(content, repository, version):
    lines = content.splitlines()
    image_index = next(
        (index for index, line in enumerate(lines) if re.match(r"^image:\s*(?:#.*)?$", line)),
        None,
    )
    if image_index is None:
        raise ValueError("Chart values have no image block")
    fields = {}
    for index in range(image_index + 1, len(lines)):
        line = lines[index]
        if line and not line[0].isspace():
            break
        match = re.match(r"^  (repository|tag):\s*.*$", line)
        if match:
            fields[match.group(1)] = index
    if set(fields) != {"repository", "tag"}:
        raise ValueError("Chart image block must contain repository and tag")
    lines[fields["repository"]] = f"  repository: {json.dumps(repository)}"
    lines[fields["tag"]] = f"  tag: {json.dumps(version)}"
    return "\n".join(lines) + "\n"


def prepare_chart(source_chart, destination, version, source_sha, repository):
    shutil.copytree(source_chart, destination)
    metadata_path = destination / "Chart.yaml"
    metadata = metadata_path.read_text()
    metadata = _replace_top_level_scalar(metadata, "version", version)
    metadata = _replace_top_level_scalar(metadata, "appVersion", version)
    metadata = _replace_annotation(
        metadata, "org.opencontainers.image.source", "https://github.com/maomada/radar"
    )
    metadata = _replace_annotation(
        metadata, "org.opencontainers.image.revision", source_sha
    )
    metadata_path.write_text(metadata)

    values_path = destination / "values.yaml"
    values_path.write_text(
        _replace_image_values(values_path.read_text(), repository, version)
    )


def package_charts(source_dir, output_dir, version, source_sha, helm="helm", runner=subprocess.run):
    validate_inputs(REPOSITORY, "refs/heads/main", source_sha, version)
    source_root = Path(source_dir)
    output_root = Path(output_dir)
    staging_root = output_root / ".staged"
    output_root.mkdir(parents=True, exist_ok=True)
    if staging_root.exists():
        shutil.rmtree(staging_root)
    staging_root.mkdir()

    archives = []
    for name, repository in CHARTS.items():
        staged_chart = staging_root / name
        prepare_chart(
            source_root / "deploy" / "helm" / name,
            staged_chart,
            version,
            source_sha,
            repository,
        )
        runner(
            [helm, "package", str(staged_chart), "--destination", str(output_root)],
            check=True,
        )
        archives.append(output_root / f"{name}-{version}.tgz")

    checksums = []
    for archive in archives:
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        checksums.append(f"{digest}  {archive.name}")
    (output_root / "SHA256SUMS").write_text("\n".join(checksums) + "\n")
    return archives


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-dir", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--helm", default="helm")
    args = parser.parse_args()
    try:
        archives = package_charts(
            args.source_dir, args.output_dir, args.version, args.sha, helm=args.helm
        )
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        print(f"Chart packaging failed: {error}", file=sys.stderr)
        return 1
    for archive in archives:
        print(archive)
    return 0


if __name__ == "__main__":
    sys.exit(main())
