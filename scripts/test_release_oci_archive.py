import hashlib
import io
import json
import tarfile
import tempfile
import unittest
from pathlib import Path

import validate_oci_archive as oci


class OCIArchiveValidationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.blobs = {}
        self.index = self._make_index()
        self.raw_manifest = json.dumps(self.index, separators=(",", ":")).encode()

    def _add_blob(self, content):
        digest = hashlib.sha256(content).hexdigest()
        self.blobs[f"blobs/sha256/{digest}"] = content
        return {"digest": f"sha256:{digest}", "size": len(content)}

    def _make_index(self, config_architecture=None):
        manifests = []
        for os_name, architecture in sorted(oci.EXPECTED_PLATFORMS):
            config = json.dumps(
                {
                    "os": os_name,
                    "architecture": config_architecture or architecture,
                },
                separators=(",", ":"),
            ).encode()
            config_descriptor = self._add_blob(config)
            manifest = json.dumps(
                {
                    "schemaVersion": 2,
                    "config": config_descriptor,
                    "layers": [],
                },
                separators=(",", ":"),
            ).encode()
            descriptor = self._add_blob(manifest)
            descriptor["platform"] = {"os": os_name, "architecture": architecture}
            manifests.append(descriptor)
        return {"schemaVersion": 2, "manifests": manifests}

    def _read_blob(self, name):
        return self.blobs[name]

    def _write_archive(self, index=None):
        archive_path = self.root / "image.oci.tar"
        layout = b'{"imageLayoutVersion":"1.0.0"}'
        index_bytes = json.dumps(index or self.index, separators=(",", ":")).encode()
        with tarfile.open(archive_path, "w") as archive:
            for name, content in [("oci-layout", layout), ("index.json", index_bytes), *self.blobs.items()]:
                info = tarfile.TarInfo(name)
                info.size = len(content)
                archive.addfile(info, io.BytesIO(content))
        return archive_path

    def test_index_requires_exact_platforms_and_matching_configs(self):
        result = oci.validate_index(self.raw_manifest, self._read_blob)
        self.assertEqual(set(result), oci.EXPECTED_PLATFORMS)

    def test_index_rejects_missing_platform(self):
        index = dict(self.index)
        index["manifests"] = index["manifests"][:1]
        with self.assertRaisesRegex(ValueError, "exactly"):
            oci.validate_index(json.dumps(index).encode(), self._read_blob)

    def test_index_rejects_extra_platform(self):
        index = json.loads(json.dumps(self.index))
        config = json.dumps(
            {"os": "linux", "architecture": "s390x"},
            separators=(",", ":"),
        ).encode()
        config_descriptor = self._add_blob(config)
        manifest = json.dumps(
            {
                "schemaVersion": 2,
                "config": config_descriptor,
                "layers": [],
            },
            separators=(",", ":"),
        ).encode()
        descriptor = self._add_blob(manifest)
        descriptor["platform"] = {"os": "linux", "architecture": "s390x"}
        index["manifests"].append(
            descriptor
        )
        with self.assertRaisesRegex(ValueError, "exactly"):
            oci.validate_index(json.dumps(index).encode(), self._read_blob)

    def test_index_rejects_config_architecture_mismatch(self):
        bad_index = self._make_index(config_architecture="amd64")
        with self.assertRaisesRegex(ValueError, "config does not match"):
            oci.validate_index(
                json.dumps(bad_index).encode(),
                self._read_blob,
            )

    def test_archive_validates_skopeo_manifest_and_platform_blobs(self):
        archive_path = self._write_archive()
        raw_path = self.root / "raw-manifest.json"
        raw_path.write_bytes(self.raw_manifest)
        result = oci.validate_archive(archive_path, raw_path)
        self.assertEqual(set(result), oci.EXPECTED_PLATFORMS)

    def test_local_copy_descriptor_preserves_source_manifest_digest(self):
        layout = self.root / "layout"
        (layout / "blobs" / "sha256").mkdir(parents=True)
        digest = hashlib.sha256(self.raw_manifest).hexdigest()
        (layout / "oci-layout").write_text('{"imageLayoutVersion":"1.0.0"}')
        (layout / "blobs" / "sha256" / digest).write_bytes(self.raw_manifest)
        (layout / "index.json").write_text(
            json.dumps(
                {
                    "schemaVersion": 2,
                    "manifests": [
                        {
                            "digest": f"sha256:{digest}",
                            "size": len(self.raw_manifest),
                            "annotations": {oci.REF_NAME_ANNOTATION: "ci-validation"},
                        }
                    ],
                }
            )
        )
        raw_path = self.root / "raw-manifest.json"
        raw_path.write_bytes(self.raw_manifest)

        result = oci.validate_layout_copy(layout, raw_path, "ci-validation")
        self.assertEqual(result, f"sha256:{digest}")

    def test_local_copy_rejects_changed_manifest_digest(self):
        layout = self.root / "layout"
        layout.mkdir()
        (layout / "oci-layout").write_text('{"imageLayoutVersion":"1.0.0"}')
        (layout / "index.json").write_text(
            json.dumps(
                {
                    "schemaVersion": 2,
                    "manifests": [
                        {
                            "digest": "sha256:" + "0" * 64,
                            "annotations": {oci.REF_NAME_ANNOTATION: "ci-validation"},
                        }
                    ],
                }
            )
        )
        raw_path = self.root / "raw-manifest.json"
        raw_path.write_bytes(self.raw_manifest)

        with self.assertRaisesRegex(ValueError, "descriptor digest"):
            oci.validate_layout_copy(layout, raw_path, "ci-validation")


if __name__ == "__main__":
    unittest.main()
