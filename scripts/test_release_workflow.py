import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class ReleaseWorkflowTests(unittest.TestCase):
    def test_release_is_manual_and_write_permission_is_publish_only(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        self.assertIn("workflow_dispatch:", workflow)
        self.assertNotRegex(workflow, r"(?m)^  (push|pull_request|schedule|release):")
        self.assertEqual(workflow.count("packages: write"), 1)
        self.assertLess(workflow.index("publish:\n"), workflow.index("packages: write"))
        self.assertIn("cancel-in-progress: false", workflow)
        self.assertIn("inputs.publish == true", workflow)
        self.assertIn("inputs.publish != true", workflow)

    def test_release_targets_and_preflight_are_owned_and_versioned(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        registry = (ROOT / "scripts/release_registry.py").read_text()
        self.assertIn("ghcr.io/maomada/radar:${{ needs.validate.outputs.version }}", workflow)
        self.assertIn("ghcr.io/maomada/radar-portal:${{ needs.validate.outputs.version }}", workflow)
        self.assertIn("ghcr.io/maomada/charts/radar:${{ needs.validate.outputs.version }}", workflow)
        self.assertIn("ghcr.io/maomada/charts/radar-portal:${{ needs.validate.outputs.version }}", workflow)
        self.assertIn("Check all four target refs before any push", workflow)
        self.assertIn("manifest_unknown", registry)
        self.assertNotIn(":latest", workflow + registry)

    def test_legacy_publisher_workflows_are_removed(self):
        removed = (
            "dev-image.yml",
            "docker-build.yml",
            "release-image.yml",
            "release-desktop.yml",
            "publish-k8s-ui.yml",
            "publish-radar-app.yml",
        )
        for name in removed:
            self.assertFalse((ROOT / ".github/workflows" / name).exists())

    def test_ci_container_build_validates_and_copies_oci_archives_locally(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        container_job = workflow.split("  container-build:\n", 1)[1]
        self.assertIn("'.dockerignore'", workflow)
        self.assertIn("linux/amd64,linux/arm64", container_job)
        self.assertIn("push: false", container_job)
        self.assertIn("outputs: type=oci,dest=", container_job)
        self.assertNotIn("type=cacheonly", container_job)
        self.assertIn("skopeo inspect --raw", container_job)
        self.assertIn("skopeo copy --all --preserve-digests", container_job)
        self.assertIn('"oci:$LAYOUT:$LAYOUT_TAG"', container_job)
        self.assertIn("python3 scripts/validate_oci_archive.py verify-copy", container_job)
        self.assertNotIn("docker://ghcr.io/", container_job)

    def test_release_build_validates_archive_with_trusted_tooling(self):
        workflow = (ROOT / ".github/workflows/release.yml").read_text()
        build_job = workflow.split("  build-images:\n", 1)[1].split("\n  charts:\n", 1)[0]
        validator = "python3 trusted/scripts/validate_oci_archive.py validate"
        self.assertIn(validator, build_job)
        self.assertLess(build_job.index(validator), build_job.index("Record archive checksum"))
        self.assertLess(build_job.index(validator), build_job.index("Upload component release archive"))


if __name__ == "__main__":
    unittest.main()
