"""Build local release archives without Git history, settings or credentials."""

import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile


ROOT = Path(__file__).resolve().parents[1]


def go(*args, env=None):
    return subprocess.check_output(["go", *args], cwd=ROOT, env=env, text=True)


def archive(entries):
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w", format=tarfile.USTAR_FORMAT) as target:
        for name, (data, mode) in sorted(entries.items()):
            info = tarfile.TarInfo(name)
            info.size, info.mode, info.mtime = len(data), mode, 0
            target.addfile(info, io.BytesIO(data))
    return gzip.compress(buffer.getvalue(), mtime=0)


def source_files(root):
    names = ["README.md", "SECURITY.md", "LICENSE", "Makefile", "go.mod", "go.sum", ".gitignore", "CONTRIBUTING.md", "CHANGELOG.md"]
    result = {}
    for name in names:
        if (root / name).is_symlink():
            raise ValueError("Release sources must not contain symlinks")
        result[name] = ((root / name).read_bytes(), 0o644)
    for directory, suffixes in {
        "cmd": {".go"}, "internal": {".go"}, "docs": {".json", ".md"},
        "scripts": {".py"}, ".github": {".yml", ".yaml", ".md"},
    }.items():
        for path in sorted((root / directory).rglob("*")):
            if path.is_symlink():
                raise ValueError("Release sources must not contain symlinks")
            if path.is_file() and path.suffix in suffixes:
                result[path.relative_to(root).as_posix()] = (path.read_bytes(), 0o644)
    return result


def dependency_notices():
    paths = sorted(set(go("list", "-mod=readonly", "-deps", "-f", "{{if .Module}}{{.Module.Path}}{{end}}", "./cmd/garmin-mcp").split()))
    entries, summary = {}, ["Licenses for modules linked into this binary:", ""]
    root_module = go("list", "-m").strip()
    for module in paths:
        if module == root_module:
            continue
        info = json.loads(go("list", "-m", "-json", module))
        directory = Path(info["Dir"])
        notices = [path for path in sorted(directory.iterdir()) if path.is_file() and path.name.upper().startswith(("LICENSE", "NOTICE", "COPYING"))]
        if not notices:
            raise ValueError("Missing third-party license for " + module)
        prefix = module.replace("/", "_") + "@" + info["Version"]
        for path in notices:
            entries["licenses/" + prefix + "/" + path.name] = (path.read_bytes(), 0o644)
        summary.append(module + " " + info["Version"])
    goroot = Path(go("env", "GOROOT").strip())
    entries["licenses/Go-LICENSE"] = ((goroot / "LICENSE").read_bytes(), 0o644)
    entries["licenses/Go-PATENTS"] = ((goroot / "PATENTS").read_bytes(), 0o644)
    entries["THIRD_PARTY_NOTICES.txt"] = (("\n".join(summary) + "\n\nGo standard library: see licenses/Go-LICENSE and licenses/Go-PATENTS.\n").encode(), 0o644)
    return entries


def main():
    destination = ROOT / "dist"
    destination.mkdir(exist_ok=True)
    notices = dependency_notices()
    sources = source_files(ROOT)
    common = {name: entry for name, entry in sources.items() if name in {"README.md", "SECURITY.md", "LICENSE", "CONTRIBUTING.md", "CHANGELOG.md"} or name.startswith("docs/")}
    outputs = {}
    for system, machine in (("darwin", "arm64"), ("darwin", "amd64"), ("linux", "amd64"), ("linux", "arm64")):
        name = "garmin-mcp_" + system + "_" + machine
        binary = destination / name
        environment = dict(os.environ, GOOS=system, GOARCH=machine, CGO_ENABLED="0")
        go("build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-ldflags=-s -w", "-o", str(binary), "./cmd/garmin-mcp", env=environment)
        entries = dict(common, **notices)
        entries["garmin-mcp"] = (binary.read_bytes(), 0o755)
        outputs[name + ".tar.gz"] = archive(entries)
        binary.unlink()
    outputs["garmin-mcp_source.tar.gz"] = archive(sources)
    for name, data in sorted(outputs.items()):
        (destination / name).write_bytes(data)
    checksums = "".join(hashlib.sha256(data).hexdigest() + "  " + name + "\n" for name, data in sorted(outputs.items()))
    (destination / "SHA256SUMS").write_text(checksums)
    print("Created four binary archives, a source archive and SHA256SUMS in dist/.")


if __name__ == "__main__":
    main()
