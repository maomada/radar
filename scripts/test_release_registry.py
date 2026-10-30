import subprocess
import unittest

import release_registry as registry


class ReleaseRegistryTests(unittest.TestCase):
    def test_release_targets_are_the_four_owned_refs(self):
        self.assertEqual(
            registry.release_targets("1.2.3"),
            (
                "ghcr.io/maomada/radar:1.2.3",
                "ghcr.io/maomada/radar-portal:1.2.3",
                "ghcr.io/maomada/charts/radar:1.2.3",
                "ghcr.io/maomada/charts/radar-portal:1.2.3",
            ),
        )

    def test_existing_manifest_blocks_release_after_checking_every_target(self):
        calls = []

        def run(command, **_):
            calls.append(command[-1])
            existing = command[-1] == "docker://ghcr.io/maomada/radar:1.2.3"
            return subprocess.CompletedProcess(
                command, 0 if existing else 1, stdout="{}", stderr="manifest unknown"
            )

        with self.assertRaisesRegex(registry.RegistryPreflightError, "already exist"):
            registry.inspect_targets("1.2.3", "/auth", runner=run)
        self.assertEqual(len(calls), 4)

    def test_explicit_manifest_unknown_allows_absence(self):
        result = registry.classify_inspection(1, stderr="MANIFEST_UNKNOWN: manifest unknown")
        self.assertEqual(result, "absent")

    def test_permission_failure_is_not_treated_as_absent(self):
        with self.assertRaises(registry.RegistryPreflightError):
            registry.classify_inspection(1, stderr="denied: requested access to the resource is denied")

    def test_network_failure_is_not_treated_as_absent(self):
        with self.assertRaises(registry.RegistryPreflightError):
            registry.classify_inspection(1, stderr="Get https://ghcr.io/v2/: i/o timeout")

    def test_unknown_failure_is_not_treated_as_absent(self):
        with self.assertRaises(registry.RegistryPreflightError):
            registry.classify_inspection(1, stderr="unexpected registry response")

    def test_inspection_timeout_blocks_release(self):
        def run(_command, **_):
            raise subprocess.TimeoutExpired("skopeo", 60)

        with self.assertRaisesRegex(registry.RegistryPreflightError, "Could not safely inspect"):
            registry.inspect_targets("1.2.3", "/auth", runner=run)


if __name__ == "__main__":
    unittest.main()
