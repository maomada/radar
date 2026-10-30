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


if __name__ == "__main__":
    unittest.main()
