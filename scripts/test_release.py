import io
from pathlib import Path
import tarfile
import tempfile
import unittest

from release import archive, source_files


class ReleaseTests(unittest.TestCase):
    def test_archive_is_deterministic_and_preserves_executable_mode(self):
        entries = {"LICENSE": (b"synthetic-license", 0o644), "garmin-mcp": (b"synthetic-binary", 0o755)}
        first = archive(entries)
        self.assertEqual(first, archive(dict(reversed(list(entries.items())))))
        with tarfile.open(fileobj=io.BytesIO(first), mode="r:gz") as result:
            self.assertEqual(result.getnames(), ["LICENSE", "garmin-mcp"])
            self.assertEqual(result.getmember("garmin-mcp").mode, 0o755)
            self.assertEqual(result.getmember("garmin-mcp").mtime, 0)

    def test_sources_exclude_private_files_builds_and_history(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name in ("README.md", "SECURITY.md", "LICENSE", "Makefile", "go.mod", "go.sum", ".gitignore", "CONTRIBUTING.md", "CHANGELOG.md", ".env", "personal-api-key"):
                (root / name).write_text("synthetic")
            for name in ("internal/example.go", "internal/private.key", "outputs/wellness.json", ".git/config", "docs/contract.json", "scripts/release.py"):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("synthetic")
            result = source_files(root)
            self.assertIn("internal/example.go", result)
            self.assertIn("docs/contract.json", result)
            for name in (".env", "personal-api-key", "internal/private.key", "outputs/wellness.json", ".git/config"):
                self.assertNotIn(name, result)
            (root / "internal/link.go").symlink_to(root / "personal-api-key")
            with self.assertRaises(ValueError):
                source_files(root)


if __name__ == "__main__":
    unittest.main()
