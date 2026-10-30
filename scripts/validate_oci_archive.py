#!/usr/bin/env python3
"""Validate multi-platform OCI archives and non-publishing OCI layout copies."""

import argparse
import hashlib
import json
import re
import sys
import tarfile
from pathlib import Path


EXPECTED_PLATFORMS = {("linux", "amd64"), ("linux", "arm64")}
REF_NAME_ANNOTATION = "org.opencontainers.image.ref.name"
NESTED_INDEX_MEDIA_TYPES = {
    "application/vnd.oci.image.index.v1+json",
    "application/vnd.docker.distribution.manifest.list.v2+json",
}


def _load_json(raw, description):
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError(f"{description} is not valid JSON") from error
    if not isinstance(value, dict):
        raise ValueError(f"{description} must be a JSON object")
    return value


def _read_descriptor(descriptor, blob_reader, description):
    if not isinstance(descriptor, dict):
        raise ValueError(f"{description} descriptor is invalid")
    digest_value = descriptor.get("digest")
    if not isinstance(digest_value, str):
        raise ValueError(f"{description} descriptor must use a SHA-256 digest")
    match = re.fullmatch(r"sha256:([0-9a-f]{64})", digest_value)
    if not match:
        raise ValueError(f"{description} descriptor must use a SHA-256 digest")
    digest = match.group(1)
    try:
        blob = blob_reader(f"blobs/sha256/{digest}")
    except (KeyError, OSError, ValueError) as error:
        raise ValueError(f"{description} blob is missing") from error
    if descriptor.get("size") != len(blob):
        raise ValueError(f"{description} blob size does not match its descriptor")
    if hashlib.sha256(blob).hexdigest() != digest:
        raise ValueError(f"{description} blob digest does not match its descriptor")
    return blob


def validate_index(index_bytes, blob_reader):
    index = _load_json(index_bytes, "OCI image index")
    if index.get("schemaVersion") != 2 or not isinstance(index.get("manifests"), list):
        raise ValueError("OCI image index must have schemaVersion 2 and a manifests array")

    platforms = {}
    for descriptor in index["manifests"]:
        if not isinstance(descriptor, dict):
            raise ValueError("OCI image index contains an invalid manifest descriptor")
        platform = descriptor.get("platform")
        if not isinstance(platform, dict):
            raise ValueError("Every OCI image descriptor must declare a platform")
        operating_system = platform.get("os")
        architecture = platform.get("architecture")
        if not isinstance(operating_system, str) or not isinstance(architecture, str):
            raise ValueError("OCI image descriptor has an invalid platform")
        platform_key = (operating_system, architecture)
        if platform_key in platforms:
            raise ValueError(f"OCI image index contains duplicate platform {operating_system}/{architecture}")

        manifest_bytes = _read_descriptor(descriptor, blob_reader, f"{operating_system}/{architecture} manifest")
        manifest = _load_json(manifest_bytes, f"{operating_system}/{architecture} image manifest")
        if manifest.get("schemaVersion") != 2:
            raise ValueError(f"{operating_system}/{architecture} image manifest must use schemaVersion 2")
        config_bytes = _read_descriptor(
            manifest.get("config"), blob_reader, f"{operating_system}/{architecture} image config"
        )
        config = _load_json(config_bytes, f"{operating_system}/{architecture} image config")
        if config.get("os") != operating_system or config.get("architecture") != architecture:
            raise ValueError(f"{operating_system}/{architecture} image config does not match its descriptor")
        platforms[platform_key] = descriptor["digest"]

    if set(platforms) != EXPECTED_PLATFORMS:
        expected = sorted(f"{os}/{architecture}" for os, architecture in EXPECTED_PLATFORMS)
        actual = sorted(f"{os}/{architecture}" for os, architecture in platforms)
        raise ValueError(f"OCI platforms must be exactly {expected}; found {actual}")
    return platforms


def _read_archive_member(archive, name):
    try:
        member = archive.getmember(name)
    except KeyError as error:
        raise ValueError(f"OCI archive is missing {name}") from error
    if not member.isfile():
        raise ValueError(f"OCI archive member {name} is not a file")
    stream = archive.extractfile(member)
    if stream is None:
        raise ValueError(f"OCI archive member {name} cannot be read")
    return stream.read()


def _validate_archive_index(index_bytes, blob_reader):
    root_index = _load_json(index_bytes, "OCI archive root index")
    descriptors = root_index.get("manifests")
    if root_index.get("schemaVersion") != 2 or not isinstance(descriptors, list):
        raise ValueError("OCI archive root index must have schemaVersion 2 and a manifests array")

    nested_indexes = [
        descriptor
        for descriptor in descriptors
        if isinstance(descriptor, dict) and descriptor.get("mediaType") in NESTED_INDEX_MEDIA_TYPES
    ]
    if nested_indexes:
        if len(descriptors) != 1 or len(nested_indexes) != 1:
            raise ValueError("OCI archive root index contains ambiguous nested image indexes")
        descriptor = nested_indexes[0]
        if "platform" in descriptor:
            raise ValueError("OCI archive root index descriptor must not declare a platform")
        platform_index = _read_descriptor(descriptor, blob_reader, "OCI archive root index")
        return validate_index(platform_index, blob_reader)

    return validate_index(index_bytes, blob_reader)


def validate_archive(archive_path, raw_manifest_path=None):
    try:
        archive = tarfile.open(archive_path, "r:*")
    except (OSError, tarfile.TarError) as error:
        raise ValueError(f"Could not open OCI archive: {error}") from error

    with archive:
        layout = _load_json(_read_archive_member(archive, "oci-layout"), "OCI layout metadata")
        if layout.get("imageLayoutVersion") != "1.0.0":
            raise ValueError("OCI layout version must be 1.0.0")
        index_bytes = _read_archive_member(archive, "index.json")

        def read_blob(name):
            return _read_archive_member(archive, name)

        platforms = _validate_archive_index(index_bytes, read_blob)
        if raw_manifest_path is not None:
            raw_manifest = Path(raw_manifest_path).read_bytes()
            raw_platforms = validate_index(raw_manifest, read_blob)
            if raw_platforms != platforms:
                raise ValueError("skopeo raw manifest does not match the OCI archive platform descriptors")
        return platforms


def validate_layout_copy(layout_path, raw_manifest_path, tag):
    layout_dir = Path(layout_path)
    layout = _load_json((layout_dir / "oci-layout").read_bytes(), "OCI layout metadata")
    if layout.get("imageLayoutVersion") != "1.0.0":
        raise ValueError("Copied OCI layout version must be 1.0.0")
    index_bytes = (layout_dir / "index.json").read_bytes()
    index = _load_json(index_bytes, "Copied OCI layout index")
    if index.get("schemaVersion") != 2 or not isinstance(index.get("manifests"), list):
        raise ValueError("Copied OCI layout index must have schemaVersion 2 and a manifests array")

    source_manifest = Path(raw_manifest_path).read_bytes()
    expected_digest = f"sha256:{hashlib.sha256(source_manifest).hexdigest()}"
    matches = [
        descriptor
        for descriptor in index["manifests"]
        if isinstance(descriptor, dict)
        and isinstance(descriptor.get("annotations"), dict)
        and descriptor["annotations"].get(REF_NAME_ANNOTATION) == tag
    ]
    if len(matches) != 1 or matches[0].get("digest") != expected_digest:
        raise ValueError("Copied OCI layout descriptor digest does not match the source raw manifest")

    def read_blob(name):
        return (layout_dir / name).read_bytes()

    copied_manifest = _read_descriptor(matches[0], read_blob, "Copied OCI manifest")
    if copied_manifest != source_manifest:
        raise ValueError("Copied OCI manifest bytes do not match the source raw manifest")
    return expected_digest


def main():
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    validate_parser = subparsers.add_parser("validate")
    validate_parser.add_argument("--archive", required=True)
    validate_parser.add_argument("--raw-manifest")

    copy_parser = subparsers.add_parser("verify-copy")
    copy_parser.add_argument("--layout", required=True)
    copy_parser.add_argument("--raw-manifest", required=True)
    copy_parser.add_argument("--tag", required=True)

    args = parser.parse_args()
    try:
        if args.command == "validate":
            platforms = validate_archive(args.archive, args.raw_manifest)
            result = {
                f"{operating_system}/{architecture}": digest
                for (operating_system, architecture), digest in sorted(platforms.items())
            }
        else:
            result = {"preservedManifestDigest": validate_layout_copy(args.layout, args.raw_manifest, args.tag)}
    except (OSError, tarfile.TarError, ValueError) as error:
        print(f"OCI validation failed: {error}", file=sys.stderr)
        return 1
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
