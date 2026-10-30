#!/usr/bin/env python3
"""Fail-closed GHCR collision checks for an owned Radar release."""

import argparse
import re
import subprocess
import sys

from release_policy import REPOSITORY, validate_inputs


class RegistryPreflightError(RuntimeError):
    pass


def release_targets(version):
    validate_inputs(REPOSITORY, "refs/heads/main", "0" * 40, version)
    return (
        f"ghcr.io/maomada/radar:{version}",
        f"ghcr.io/maomada/radar-portal:{version}",
        f"ghcr.io/maomada/charts/radar:{version}",
        f"ghcr.io/maomada/charts/radar-portal:{version}",
    )


def classify_inspection(returncode, stdout="", stderr=""):
    if returncode == 0:
        return "present"

    message = f"{stdout}\n{stderr}".lower()
    unsafe_markers = (
        "unauthorized",
        "authentication required",
        "denied",
        "forbidden",
        "timed out",
        "timeout",
        "connection refused",
        "no such host",
        "temporary failure",
        "network is unreachable",
        "tls handshake",
    )
    if any(marker in message for marker in unsafe_markers):
        raise RegistryPreflightError("Registry inspection failed; absence was not established")

    absent_markers = (
        r"\bmanifest_unknown\b",
        r"\bname_unknown\b",
        r"\bmanifest\s+(?:is\s+)?unknown\b",
        r"\bmanifest\s+not\s+found\b",
        r"\bname\s+(?:is\s+)?unknown\b",
        r"\bname\s+not\s+found\b",
        r"\bno\s+such\s+manifest\b",
    )
    if any(re.search(marker, message) for marker in absent_markers):
        return "absent"
    raise RegistryPreflightError("Registry inspection failed; absence was not established")


def inspect_targets(version, authfile, runner=subprocess.run):
    results = []
    collisions = []
    failures = []
    for ref in release_targets(version):
        try:
            result = runner(
                [
                    "skopeo",
                    "inspect",
                    "--raw",
                    "--authfile",
                    authfile,
                    f"docker://{ref}",
                ],
                check=False,
                capture_output=True,
                text=True,
                timeout=60,
            )
            status = classify_inspection(result.returncode, result.stdout, result.stderr)
        except (OSError, subprocess.TimeoutExpired, RegistryPreflightError):
            status = "inspection-failed"
            failures.append(ref)
        if status == "present":
            collisions.append(ref)
        results.append({"ref": ref, "status": status})

    if failures:
        raise RegistryPreflightError(
            "Could not safely inspect all release targets: " + ", ".join(failures)
        )
    if collisions:
        raise RegistryPreflightError(
            "Release blocked because target refs already exist: " + ", ".join(collisions)
        )
    return results


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True)
    parser.add_argument("--authfile", required=True)
    args = parser.parse_args()
    try:
        results = inspect_targets(args.version, args.authfile)
    except RegistryPreflightError as error:
        print(f"Release blocked: {error}", file=sys.stderr)
        return 1
    for item in results:
        print(f"Absent target confirmed: {item['ref']}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
