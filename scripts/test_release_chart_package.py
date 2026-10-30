import tempfile
import unittest
from pathlib import Path

import release_chart_package as package


ROOT = Path(__file__).resolve().parents[1]
SHA = "a" * 40


class ReleaseChartPackageTests(unittest.TestCase):
    def test_staged_charts_stamp_version_repositories_and_source_revision(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "charts"
            def package_fixture(command, **_kwargs):
                chart = Path(command[2])
                name = chart.name
                (Path(command[4]) / f"{name}-0.1.0-test.1.tgz").write_bytes(name.encode())

            archives = package.package_charts(
                ROOT, output, "0.1.0-test.1", SHA, helm="helm", runner=package_fixture
            )
            self.assertEqual(
                [archive.name for archive in archives],
                ["radar-0.1.0-test.1.tgz", "radar-portal-0.1.0-test.1.tgz"],
            )
            for name, repository in package.CHARTS.items():
                chart = output / ".staged" / name
                metadata = (chart / "Chart.yaml").read_text()
                values = (chart / "values.yaml").read_text()
                self.assertIn('version: "0.1.0-test.1"', metadata)
                self.assertIn('appVersion: "0.1.0-test.1"', metadata)
                self.assertIn(
                    f'org.opencontainers.image.source: "https://github.com/maomada/radar"',
                    metadata,
                )
                self.assertIn(f'org.opencontainers.image.revision: "{SHA}"', metadata)
                self.assertIn(f'repository: "{repository}"', values)
                self.assertIn('tag: "0.1.0-test.1"', values)
            self.assertEqual(len((output / "SHA256SUMS").read_text().splitlines()), 2)

    def test_invalid_source_sha_is_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaises(ValueError):
                package.package_charts(ROOT, temporary, "1.2.3", "not-a-sha")


if __name__ == "__main__":
    unittest.main()
