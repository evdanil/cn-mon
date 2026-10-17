#!/usr/bin/env python3
# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0
"""Resolve a published cn-mon release and submit a package-only Store update.

This intentionally uses the Python standard library and exposes the HTTP
request seam for deterministic offline tests. Sensitive values are never
included in exceptions or command output.
"""
from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import zipfile
from pathlib import Path, PurePosixPath

REPO = "evdanil/cn-mon"
GITHUB = "https://api.github.com"
STORE = "https://manage.devcenter.microsoft.com/v1.0/my"
TOKEN_HOST = "https://login.microsoftonline.com"
FLOOR = "1.3.2.0"
MAX_ASSET = 200 * 1024 * 1024
MAX_ZIP_ENTRY = 100 * 1024 * 1024
MAX_PACKAGE = 500 * 1024 * 1024
MAKEAPPX_FOOTPRINT = {"[Content_Types].xml", "AppxBlockMap.xml"}
WIN_FILES = {"cn-mon.exe", "README.md", "LICENSE", "THIRD_PARTY_NOTICES.md"}
MSIX_FILES = WIN_FILES | {"cn-mon.exe.manifest", "README-MSIX.md", "Assets/StoreLogo.png", "Assets/Square44x44Logo.png", "Assets/Square150x150Logo.png", "AppxManifest.xml"}
TOKEN = re.compile(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)\Z")
SHA = re.compile(r"[0-9a-f]{64}\Z")
FULL_SHA = re.compile(r"[0-9a-f]{40}\Z")
MARKER_PREFIX = "CNMON_STORE_UPDATE:"
PENDING_STATUSES = {"PendingCommit", "CommitStarted", "PendingPublication", "PreProcessing", "Certification", "Release", "Publishing"}
ACCEPTED_STATUSES = {"PreProcessing", "Certification", "Release", "Publishing", "PendingPublication", "Published"}
FAILED_STATUSES = {"CommitFailed", "PreProcessingFailed", "CertificationFailed", "ReleaseFailed", "PublishFailed", "Canceled"}


class ControllerError(Exception):
    """Safe fixed-message controller failure."""

    def __init__(self, code="failed"):
        self.code = code if code in {"failed", "missing_credentials", "pending_submission", "no_published_baseline", "published_state_changed", "unsupported_pricing"} else "failed"
        super().__init__(self.code)


def fail(code="failed"):
    raise ControllerError(code)


def request(method, url, headers=None, body=None, timeout=30):
    """Production transport. HTTP response bodies are not surfaced on errors."""
    initial = urllib.parse.urlparse(url)
    allow_redirect = method == "GET" and initial.scheme == "https" and initial.hostname == "github.com" and initial.path.startswith("/evdanil/cn-mon/releases/download/")

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, response_headers, newurl):
            return None

    try:
        opener = urllib.request.build_opener(NoRedirect)
        req = urllib.request.Request(url, data=body, headers=headers or {}, method=method)
        try:
            response = opener.open(req, timeout=timeout)
        except urllib.error.HTTPError as error:
            if not allow_redirect or error.code not in (301, 302, 303, 307, 308):
                fail()
            target = urllib.parse.urljoin(url, error.headers.get("Location", ""))
            parsed = urllib.parse.urlparse(target)
            if parsed.scheme != "https" or parsed.hostname not in ("release-assets.githubusercontent.com", "objects.githubusercontent.com"):
                fail()
            req = urllib.request.Request(target, headers={"User-Agent": "cn-mon-store-update"}, method="GET")
            response = opener.open(req, timeout=timeout)
        with response:
            if response.status < 200 or response.status >= 300:
                fail()
            return response.status, response.read(MAX_ASSET + 1), dict(response.headers)
    except (urllib.error.URLError, TimeoutError, OSError):
        fail()


def _json(data):
    try:
        return json.loads(data.decode("utf-8") if isinstance(data, bytes) else data)
    except (ValueError, UnicodeError, AttributeError):
        fail()


def _api(client, method, url, token=None, data=None, content_type="application/json"):
    headers = {"Accept": "application/json", "User-Agent": "cn-mon-store-update"}
    if token:
        headers["Authorization"] = "Bearer " + token
    body = None
    if data is not None:
        headers["Content-Type"] = content_type
        body = data if isinstance(data, bytes) else json.dumps(data, separators=(",", ":")).encode()
    status, payload, response_headers = client(method, url, headers, body)
    if status < 200 or status >= 300 or len(payload) > MAX_ASSET:
        fail()
    return _json(payload) if payload else {}, response_headers


def _semver(tag):
    match = TOKEN.fullmatch(tag or "")
    if not match:
        fail()
    values = tuple(int(part) for part in match.groups())
    if any(v > 65535 for v in values):
        fail()
    return values


def _asset_digest(asset):
    digest = asset.get("digest")
    if not isinstance(digest, str) or not digest.startswith("sha256:") or not SHA.fullmatch(digest[7:]):
        fail()
    return digest[7:]


def _get_json(client, url):
    obj, _ = _api(client, "GET", url)
    return obj


def _download(client, url, max_size=MAX_ASSET):
    if not url.startswith("https://github.com/") and not url.startswith("https://api.github.com/"):
        fail()
    status, payload, _ = client("GET", url, {"Accept": "application/octet-stream", "User-Agent": "cn-mon-store-update"}, None)
    if status < 200 or status >= 300 or len(payload) > max_size:
        fail()
    return payload


def _validate_windows_zip(data, root):
    try:
        from io import BytesIO
        with zipfile.ZipFile(BytesIO(data)) as zf:
            seen = set()
            total = 0
            binary_hash = None
            prefix = root + "/"
            for info in zf.infolist():
                path = PurePosixPath(info.filename)
                if path.is_absolute() or ".." in path.parts or "\\" in info.filename or info.is_dir():
                    fail()
                if ((info.external_attr >> 16) & 0o170000) == 0o120000:
                    fail()
                if not info.filename.startswith(prefix):
                    fail()
                name = info.filename[len(prefix):]
                if name not in WIN_FILES or name in seen or "/" in name:
                    fail()
                if info.file_size > MAX_ZIP_ENTRY:
                    fail()
                total += info.file_size
                if total > MAX_ASSET:
                    fail()
                seen.add(name)
                if name == "cn-mon.exe":
                    hasher = hashlib.sha256()
                    with zf.open(info) as stream:
                        for block in iter(lambda: stream.read(1024 * 1024), b""):
                            hasher.update(block)
                    binary_hash = hasher.hexdigest()
            if seen != WIN_FILES or not binary_hash:
                fail()
            return binary_hash
    except (zipfile.BadZipFile, RuntimeError, OSError):
        fail()


def resolve_release(tag, github_client=request, source_sha=None):
    """Return validated public metadata from the canonical GitHub repository."""
    version = _semver(tag)
    if source_sha is not None and not FULL_SHA.fullmatch(source_sha or ""):
        fail()
    base = GITHUB + "/repos/" + REPO
    release = _get_json(github_client, base + "/releases/tags/" + urllib.parse.quote(tag, safe=""))
    if release.get("draft") is not False or release.get("prerelease") is not False or release.get("tag_name") != tag:
        fail()
    release_id = release.get("id")
    if not isinstance(release_id, int) or release_id <= 0:
        fail()
    assets = release.get("assets")
    if not isinstance(assets, list):
        fail()
    root = "cn-mon_" + ".".join(map(str, version)) + "_windows_amd64"
    win_name = root + ".zip"
    linux_name = "cn-mon_" + ".".join(map(str, version)) + "_linux_amd64.tar.gz"
    required = {win_name, linux_name, "SHA256SUMS"}
    by_name = {}
    for asset in assets:
        name = asset.get("name") if isinstance(asset, dict) else None
        if name in required:
            if name in by_name:
                fail()
            by_name[name] = asset
    if set(by_name) != required:
        fail()
    tag_ref = _get_json(github_client, base + "/git/ref/tags/" + urllib.parse.quote(tag, safe=""))
    obj = tag_ref.get("object", {})
    depth = 0
    while obj.get("type") == "tag":
        depth += 1
        if depth > 8 or not FULL_SHA.fullmatch(str(obj.get("sha", ""))):
            fail()
        obj = _get_json(github_client, base + "/git/tags/" + obj["sha"]).get("object", {})
    tag_commit = obj.get("sha")
    if obj.get("type") != "commit" or not FULL_SHA.fullmatch(str(tag_commit or "")):
        fail()
    comparison = _get_json(github_client, base + "/compare/main..." + tag_commit)
    if comparison.get("status") not in {"behind", "identical"}:
        fail()
    # Require server-provided canonical asset URLs and compare asset payloads to
    # both GitHub's digest and the release's checksum manifest.
    downloads = {}
    for name in sorted(required):
        asset = by_name[name]
        if not isinstance(asset.get("id"), int) or asset["id"] <= 0:
            fail()
        url = asset.get("browser_download_url")
        expected_url = "https://github.com/" + REPO + "/releases/download/" + urllib.parse.quote(tag, safe="") + "/" + urllib.parse.quote(name, safe="")
        if url != expected_url:
            fail()
        payload = _download(github_client, url)
        digest = hashlib.sha256(payload).hexdigest()
        if digest != _asset_digest(asset):
            fail()
        downloads[name] = (asset, payload, digest)
    sums = downloads["SHA256SUMS"][1].decode("ascii", "strict").splitlines()
    parsed = {}
    for line in sums:
        m = re.fullmatch(r"([0-9a-f]{64}) [ *]([^/\\]+)", line)
        if not m or m.group(2) in parsed:
            fail()
        parsed[m.group(2)] = m.group(1)
    if set(parsed) != {win_name, linux_name} or parsed.get(win_name) != downloads[win_name][2] or parsed.get(linux_name) != downloads[linux_name][2]:
        fail()
    # Binary hash is pinned by the ZIP; archive verifier and Windows runtime
    # qualification independently verify the executable's reported version.
    binary_sha = _validate_windows_zip(downloads[win_name][1], root)
    archive_sha = downloads[win_name][2]
    resolved = {
        "schema_version": 1,
        "archive_url": "https://github.com/" + REPO + "/releases/download/" + tag + "/" + win_name,
        "archive_name": win_name, "archive_root": root,
        "cli_version": ".".join(map(str, version)), "cli_revision": tag_commit,
        "archive_sha256": archive_sha, "binary_sha256": binary_sha,
        "package_version": None, "release_id": release_id, "release_tag": tag,
        "release_commit": tag_commit, "source_repository": REPO,
        "release_assets": {name: {"id": by_name[name]["id"], "digest": downloads[name][2]} for name in sorted(required)},
        "packaging_revision": source_sha,
    }
    _validate_release_metadata(resolved)
    return resolved


def _parse_package_version(value):
    if not isinstance(value, str):
        fail()
    pieces = value.split(".")
    if len(pieces) != 4 or any(not p.isdigit() or (len(p) > 1 and p[0] == "0") for p in pieces):
        fail()
    nums = tuple(int(p) for p in pieces)
    if any(n > 65535 for n in nums) or nums[0] == 0 or nums[3] != 0:
        fail()
    return nums


def next_package_version(versions):
    values = [_parse_package_version(FLOOR)]
    values.extend(_parse_package_version(item) for item in versions)
    a, b, c, _ = max(values)
    if c < 65535:
        c += 1
    else:
        c = 0
        if b < 65535:
            b += 1
        else:
            b = 0
            if a == 65535:
                fail()
            a += 1
    return f"{a}.{b}.{c}.0"


def _marker(release):
    return {"schema": 1, "release_id": release["release_id"], "tag": release["release_tag"],
            "commit": release["release_commit"], "archive_sha256": release["archive_sha256"],
            "packaging_revision": release["packaging_revision"], "package_version": release["package_version"]}


def _validate_release_metadata(release, require_package=False):
    if not isinstance(release, dict) or release.get("schema_version") != 1 or release.get("source_repository") != REPO:
        fail()
    parts = _semver(release.get("release_tag"))
    version = ".".join(map(str, parts))
    root = "cn-mon_" + version + "_windows_amd64"
    name = root + ".zip"
    if (release.get("cli_version") != version or release.get("cli_revision") != release.get("release_commit") or
            not FULL_SHA.fullmatch(str(release.get("release_commit", ""))) or release.get("archive_name") != name or
            release.get("archive_root") != root or release.get("archive_url") != "https://github.com/" + REPO + "/releases/download/" + release["release_tag"] + "/" + name or
            not isinstance(release.get("release_id"), int) or release["release_id"] <= 0 or
            not SHA.fullmatch(str(release.get("archive_sha256", ""))) or not SHA.fullmatch(str(release.get("binary_sha256", ""))) or
            not isinstance(release.get("release_assets"), dict)):
        fail()
    expected_assets = {name, f"cn-mon_{version}_linux_amd64.tar.gz", "SHA256SUMS"}
    assets = release["release_assets"]
    if set(assets) != expected_assets:
        fail()
    for asset in assets.values():
        if (not isinstance(asset, dict) or not isinstance(asset.get("id"), int) or asset["id"] <= 0 or
                not SHA.fullmatch(str(asset.get("digest", "")))):
            fail()
    if assets[name].get("digest") != release["archive_sha256"]:
        fail()
    if release.get("packaging_revision") is not None and not FULL_SHA.fullmatch(str(release["packaging_revision"])):
        fail()
    if require_package:
        _parse_package_version(release.get("package_version"))
        if release.get("msix_name") != "cn-mon_" + release["package_version"] + "_x64.msix" or not FULL_SHA.fullmatch(str(release.get("packaging_revision", ""))):
            fail()
    elif release.get("package_version") is not None:
        _parse_package_version(release["package_version"])


def _marker_from_notes(notes):
    found = []
    for line in notes.splitlines():
        if line.startswith(MARKER_PREFIX):
            try:
                marker = json.loads(line[len(MARKER_PREFIX):])
            except ValueError:
                fail()
            if (not isinstance(marker, dict) or marker.get("schema") != 1 or
                    not isinstance(marker.get("release_id"), int) or marker["release_id"] <= 0 or
                    not isinstance(marker.get("tag"), str) or not isinstance(marker.get("commit"), str) or
                    not FULL_SHA.fullmatch(marker["commit"]) or not isinstance(marker.get("archive_sha256"), str) or
                    not SHA.fullmatch(marker["archive_sha256"]) or not isinstance(marker.get("packaging_revision"), str) or
                    not FULL_SHA.fullmatch(marker["packaging_revision"])):
                fail()
            _semver(marker["tag"])
            _parse_package_version(marker.get("package_version"))
            found.append(marker)
    if len(found) > 1:
        fail()
    return found[0] if found else None


def prepare_submission(published, release):
    """Clone published metadata, changing only package states, notes, publish mode."""
    if not isinstance(published, dict) or published.get("status") not in ("Published", "PendingCommit") or not published.get("id"):
        fail()
    packages = published.get("applicationPackages")
    if not isinstance(packages, list) or not packages:
        fail()
    updated = copy.deepcopy(published)
    for package in updated["applicationPackages"]:
        architecture = package.get("architecture")
        if architecture not in ("X64", "x64", "x86_64"):
            fail()
        if not isinstance(package.get("fileName"), str) or not package["fileName"].lower().endswith((".msix", ".appx")):
            fail()
        package["fileStatus"] = "PendingDelete"
    version = release.get("package_version")
    _parse_package_version(version)
    expected_name = "cn-mon_" + version + "_x64.msix"
    if release.get("msix_name") != expected_name:
        fail()
    updated["applicationPackages"].append({"fileName": expected_name, "fileStatus": "PendingUpload", "minimumDirectXVersion": "None", "minimumSystemRam": "None"})
    notes_field = "notesForCertification"
    notes = updated.get(notes_field, "")
    if not isinstance(notes, str):
        fail()
    _marker_from_notes(notes)
    cleaned = re.sub(r"(?m)^CNMON_STORE_UPDATE:[^\r\n]*(?:\r\n|\n|\r)?", "", notes)
    marker = MARKER_PREFIX + json.dumps(_marker(release), sort_keys=True, separators=(",", ":"))
    updated[notes_field] = cleaned + ("" if not cleaned or cleaned.endswith(("\n", "\r")) else "\n") + marker
    updated["targetPublishMode"] = "Immediate"
    return updated


def _credentials():
    names = ("CNMON_STORE_APP_ID", "CNMON_STORE_TENANT_ID", "CNMON_STORE_CLIENT_ID", "CNMON_STORE_CLIENT_SECRET",
             "CNMON_MSIX_IDENTITY_NAME", "CNMON_MSIX_IDENTITY_PUBLISHER", "CNMON_MSIX_PUBLISHER_DISPLAY_NAME", "CNMON_MSIX_EXPECTED_PFN")
    vals = {name: os.environ.get(name, "") for name in names}
    if any(not value for value in vals.values()):
        fail("missing_credentials")
    return vals


class StoreClient:
    def __init__(self, credentials, transport=request):
        self.c = credentials
        self.transport = transport
        self.token = self._token()
        self.base = STORE + "/applications/" + urllib.parse.quote(credentials["CNMON_STORE_APP_ID"], safe="")

    def _token(self):
        c = self.c
        form = urllib.parse.urlencode({"grant_type": "client_credentials", "client_id": c["CNMON_STORE_CLIENT_ID"],
            "client_secret": c["CNMON_STORE_CLIENT_SECRET"], "resource": "https://manage.devcenter.microsoft.com"}).encode()
        obj, _ = _api(self.transport, "POST", TOKEN_HOST + "/" + urllib.parse.quote(c["CNMON_STORE_TENANT_ID"], safe="") + "/oauth2/token", data=form, content_type="application/x-www-form-urlencoded")
        token = obj.get("access_token")
        if not isinstance(token, str) or not token:
            fail()
        return token

    def get(self, path):
        return _api(self.transport, "GET", self.base + path, self.token)[0]

    def write(self, method, path, data=None):
        return _api(self.transport, method, self.base + path, self.token, data)[0]

    def app(self):
        return self.get("")


def _check_app_identity(app, credentials):
    expected = {"packageIdentityName": credentials["CNMON_MSIX_IDENTITY_NAME"],
                "publisherName": credentials["CNMON_MSIX_IDENTITY_PUBLISHER"],
                "packageFamilyName": credentials["CNMON_MSIX_EXPECTED_PFN"]}
    if any(app.get(k) != v for k, v in expected.items()):
        fail()


def _submission_id(app, key):
    sub = app.get(key)
    if not isinstance(sub, dict) or not isinstance(sub.get("id"), (str, int)) or not sub.get("id"):
        fail("no_published_baseline")
    return str(sub["id"])


def _has_pending_submission(app):
    return app.get("pendingApplicationSubmission") is not None


def _validate_published_packages(published):
    packages = published.get("applicationPackages")
    if not isinstance(packages, list) or not packages:
        fail()
    for package in packages:
        if not isinstance(package, dict) or package.get("architecture") not in ("X64", "x64", "x86_64"):
            fail()
        if not isinstance(package.get("fileName"), str) or not package["fileName"].lower().endswith((".msix", ".appx")):
            fail()
        _parse_package_version(package.get("version"))
    return packages


def _validate_pricing(submission):
    pricing = submission.get("pricing") if isinstance(submission, dict) else None
    price_id = pricing.get("priceId") if isinstance(pricing, dict) else None
    if not isinstance(price_id, str) or not price_id.strip() or price_id.strip().casefold() == "base":
        fail("unsupported_pricing")
    return pricing


def _read_preflight(client, release):
    app = client.app()
    _check_app_identity(app, client.c)
    if _has_pending_submission(app):
        fail("pending_submission")
    published_id = _submission_id(app, "lastPublishedApplicationSubmission")
    published = client.get("/submissions/" + urllib.parse.quote(published_id, safe=""))
    if published.get("status") != "Published":
        fail("no_published_baseline")
    _validate_pricing(published)
    _validate_published_packages(published)
    notes = published.get("notesForCertification", "")
    if not isinstance(notes, str):
        fail()
    prior = _marker_from_notes(notes)
    if prior:
        same_binding = all(prior.get(key) == release.get(value) for key, value in (
            ("release_id", "release_id"), ("tag", "release_tag"), ("commit", "release_commit"),
            ("archive_sha256", "archive_sha256"), ("packaging_revision", "packaging_revision")))
        if same_binding:
            if prior.get("package_version") != release.get("package_version") or not any(
                    p.get("version") == prior.get("package_version") and p.get("architecture") in ("X64", "x64", "x86_64") for p in published.get("applicationPackages", [])):
                fail()
            return app, published_id, published, "already_published"
        if tuple(_semver(release["release_tag"])) < tuple(_semver(prior.get("tag", ""))):
            fail()
    elif tuple(_semver(release["release_tag"])) < (0, 3, 0):
        fail()
    return app, published_id, published, None


def _json_file(path):
    try:
        return _json(Path(path).read_bytes())
    except OSError:
        fail()


def _write_json(path, value):
    target = Path(path)
    resolved = target.resolve()
    roots = [Path.cwd().resolve()]
    if os.environ.get("GITHUB_WORKSPACE"):
        roots.append(Path(os.environ["GITHUB_WORKSPACE"]).resolve())
    if any(resolved == root or root in resolved.parents for root in roots):
        fail()
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_text(json.dumps(value, sort_keys=True, indent=2) + "\n", encoding="utf-8")


def _published_fingerprint(published):
    stable = copy.deepcopy(published)
    for key in ("fileUploadUrl", "statusDetails"):
        stable.pop(key, None)
    return hashlib.sha256(json.dumps(stable, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def _preflight(release, source_sha, *, transport=request):
    if not FULL_SHA.fullmatch(source_sha or ""):
        fail()
    release = copy.deepcopy(release)
    release["packaging_revision"] = source_sha
    _validate_release_metadata(release)
    client = StoreClient(_credentials(), transport)
    app = client.app()
    _check_app_identity(app, client.c)
    if _has_pending_submission(app):
        fail("pending_submission")
    published_id = _submission_id(app, "lastPublishedApplicationSubmission")
    published = client.get("/submissions/" + urllib.parse.quote(published_id, safe=""))
    if published.get("status") != "Published":
        fail("no_published_baseline")
    _validate_pricing(published)
    packages = _validate_published_packages(published)
    latest = [package["version"] for package in packages]
    notes = published.get("notesForCertification", "")
    if not isinstance(notes, str):
        fail()
    marker = _marker_from_notes(notes)
    if marker and all(marker.get(key) == release.get(release_key) for key, release_key in (
            ("release_id", "release_id"), ("tag", "release_tag"), ("commit", "release_commit"),
            ("archive_sha256", "archive_sha256"), ("packaging_revision", "packaging_revision"))):
        version = marker.get("package_version")
        _parse_package_version(version)
        if not any(p.get("version") == version and p.get("architecture") in ("X64", "x64", "x86_64") for p in packages):
            fail()
        release["package_version"] = version
        release["msix_name"] = "cn-mon_" + version + "_x64.msix"
        state = {"schema_version": 1, "status": "already_published", "published_submission_id": published_id,
                 "published_submission_sha256": _published_fingerprint(published),
                 "package_version": version, "release_binding": {k: release[k] for k in ("release_id", "release_tag", "release_commit", "archive_sha256", "packaging_revision")}}
        return release, state
    version = next_package_version(latest)
    release["package_version"] = version
    release["msix_name"] = "cn-mon_" + version + "_x64.msix"
    if marker and marker.get("tag") and tuple(_semver(release["release_tag"])) < tuple(_semver(marker["tag"])):
        fail()
    if not marker and tuple(_semver(release["release_tag"])) < (0, 3, 0):
        fail()
    state = {"schema_version": 1, "status": "ready", "published_submission_id": published_id,
             "published_submission_sha256": _published_fingerprint(published),
             "package_version": version, "release_binding": {k: release[k] for k in ("release_id", "release_tag", "release_commit", "archive_sha256", "packaging_revision")}}
    return release, state


def _verify_receipt(release, package_path, receipt_path, source_sha):
    _validate_release_metadata(release, require_package=True)
    if not FULL_SHA.fullmatch(source_sha or "") or release.get("packaging_revision") != source_sha:
        fail()
    try:
        if Path(package_path).stat().st_size > MAX_PACKAGE:
            fail()
        package = Path(package_path).read_bytes()
        receipt = _json_file(receipt_path)
    except OSError:
        fail()
    if Path(package_path).name != release.get("msix_name") or not package or len(package) > MAX_PACKAGE or not zipfile.is_zipfile(package_path):
        fail()
    package_hash = hashlib.sha256(package).hexdigest()
    # The qualification receipt must bind the exact unsigned package and source.
    bindings = {"package_sha256": package_hash, "packaging_revision": source_sha,
                "package_version": release["package_version"], "archive_sha256": release["archive_sha256"],
                "binary_sha256": release["binary_sha256"]}
    for key, value in bindings.items():
        if not isinstance(receipt, dict) or receipt.get(key) != value:
            fail()
    credentials = _credentials()
    if (receipt.get("schema_version") != 1 or receipt.get("purpose") != "candidate" or
            receipt.get("signing") != "unsigned" or receipt.get("architecture") != "x64" or
            receipt.get("name") != credentials["CNMON_MSIX_IDENTITY_NAME"] or
            receipt.get("publisher") != credentials["CNMON_MSIX_IDENTITY_PUBLISHER"] or
            receipt.get("publisher_display_name") != credentials["CNMON_MSIX_PUBLISHER_DISPLAY_NAME"] or
            receipt.get("expected_pfn") != credentials["CNMON_MSIX_EXPECTED_PFN"] or
            receipt.get("cli_version") != release.get("cli_version") or
            receipt.get("cli_revision") != release.get("cli_revision")):
        fail()
    files = receipt.get("files")
    if (not isinstance(files, dict) or set(files) != MSIX_FILES or files.get("cn-mon.exe") != release.get("binary_sha256") or
            files.get("AppxManifest.xml") != receipt.get("manifest_sha256") or
            any(not isinstance(name, str) or not isinstance(digest, str) or not SHA.fullmatch(digest) for name, digest in files.items())):
        fail()
    try:
        with zipfile.ZipFile(package_path) as zf:
            infos = zf.infolist()
            names = [info.filename for info in infos]
            if (len(names) != len(set(names)) or any(PurePosixPath(n).is_absolute() or ".." in PurePosixPath(n).parts or "\\" in n for n in names) or
                    "AppxSignature.p7x" in names or not set(files).issubset(names) or set(names) - set(files) - MAKEAPPX_FOOTPRINT):
                fail()
            if (sum(info.file_size for info in infos) > MAX_PACKAGE or
                    any(info.file_size > MAX_ZIP_ENTRY or ((info.external_attr >> 16) & 0o170000) == 0o120000 for info in infos)):
                fail()
            total_size = 0
            info_by_name = {info.filename: info for info in infos}
            for name, expected in files.items():
                info = info_by_name[name]
                total_size += info.file_size
                if info.file_size > MAX_ZIP_ENTRY or total_size > MAX_PACKAGE or ((info.external_attr >> 16) & 0o170000) == 0o120000:
                    fail()
                h = hashlib.sha256()
                with zf.open(info) as stream:
                    for block in iter(lambda: stream.read(1024 * 1024), b""):
                        h.update(block)
                if h.hexdigest() != expected:
                    fail()
            manifest_bytes = zf.read("AppxManifest.xml")
            if hashlib.sha256(zf.read("cn-mon.exe")).hexdigest() != release["binary_sha256"] or hashlib.sha256(manifest_bytes).hexdigest() != receipt["manifest_sha256"]:
                fail()
            import xml.etree.ElementTree as ET
            root = ET.fromstring(manifest_bytes)
            ns = "{http://schemas.microsoft.com/appx/manifest/foundation/windows10}"
            identity = root.find(ns + "Identity")
            display = root.findtext(ns + "Properties/" + ns + "DisplayName")
            publisher_display = root.findtext(ns + "Properties/" + ns + "PublisherDisplayName")
            visual = root.find(".//{http://schemas.microsoft.com/appx/manifest/uap/windows10}VisualElements")
            if (identity is None or identity.attrib.get("Name") != credentials["CNMON_MSIX_IDENTITY_NAME"] or
                    identity.attrib.get("Publisher") != credentials["CNMON_MSIX_IDENTITY_PUBLISHER"] or
                    identity.attrib.get("Version") != release["package_version"] or identity.attrib.get("ProcessorArchitecture") != "x64" or
                    display != "cn-mon — Network Monitor" or publisher_display != credentials["CNMON_MSIX_PUBLISHER_DISPLAY_NAME"] or
                    visual is None or visual.attrib.get("DisplayName") != "cn-mon — Network Monitor"):
                fail()
    except (OSError, KeyError, zipfile.BadZipFile, RuntimeError, ValueError):
        fail()
    return package


def _revalidate_release(release, github_client=request):
    current = resolve_release(release["release_tag"], github_client, release["packaging_revision"])
    for field in ("schema_version", "archive_url", "archive_name", "archive_root", "cli_version", "cli_revision", "archive_sha256", "binary_sha256", "release_id", "release_tag", "release_commit", "source_repository", "release_assets", "packaging_revision"):
        if current.get(field) != release.get(field):
            fail()


def _safe_upload_url(url):
    parsed = urllib.parse.urlparse(url or "")
    if (parsed.scheme != "https" or not parsed.hostname or not parsed.hostname.endswith(".blob.core.windows.net") or
            parsed.port not in (None, 443) or parsed.username or parsed.password or parsed.fragment or not parsed.query):
        fail()
    return url


def _status_name(client, submission_id):
    obj = client.get("/submissions/" + urllib.parse.quote(submission_id, safe="") + "/status")
    status = obj.get("status")
    if status in FAILED_STATUSES:
        fail()
    if status not in ACCEPTED_STATUSES | PENDING_STATUSES:
        fail()
    return status


def submit(release, state, package_path, receipt_path, source_sha, *, transport=request, github_client=request, poll_seconds=120):
    _validate_release_metadata(release, require_package=True)
    expected_binding = {k: release.get(k) for k in ("release_id", "release_tag", "release_commit", "archive_sha256", "packaging_revision")}
    if (not isinstance(state, dict) or state.get("schema_version") != 1 or
            state.get("status") not in ("ready", "already_published") or
            state.get("release_binding") != expected_binding or
            state.get("package_version") != release.get("package_version") or
            release.get("packaging_revision") != source_sha):
        fail()
    package = _verify_receipt(release, package_path, receipt_path, source_sha)
    _revalidate_release(release, github_client)
    c = _credentials()
    client = StoreClient(c, transport)
    app = client.app()
    _check_app_identity(app, c)
    if _has_pending_submission(app):
        fail("pending_submission")
    published_id = _submission_id(app, "lastPublishedApplicationSubmission")
    published = client.get("/submissions/" + urllib.parse.quote(published_id, safe=""))
    digest = _published_fingerprint(published)
    if published_id != state.get("published_submission_id") or digest != state.get("published_submission_sha256"):
        fail("published_state_changed")
    fresh_app, fresh_published_id, fresh_published, duplicate = _read_preflight(client, release)
    if fresh_published_id != state.get("published_submission_id") or _published_fingerprint(fresh_published) != state.get("published_submission_sha256"):
        fail("published_state_changed")
    if duplicate == "already_published":
        return "already_published"
    if state.get("status") != "ready":
        fail()
    # POST has ambiguous outcome on timeout; this is executed once and errors
    # are never retried. All subsequent operations require a valid response.
    created = client.write("POST", "/submissions")
    if not isinstance(created, dict) or created.get("status") != "PendingCommit":
        fail()
    submission_id = created.get("id")
    upload_url = _safe_upload_url(created.get("fileUploadUrl"))
    if not isinstance(submission_id, (str, int)) or not str(submission_id):
        fail()
    cloned = created
    clone_packages = _validate_published_packages(cloned)
    if next_package_version([p["version"] for p in clone_packages]) != release["package_version"]:
        fail("published_state_changed")
    _validate_pricing(cloned)
    updated = prepare_submission(cloned, release)
    client.write("PUT", "/submissions/" + urllib.parse.quote(str(submission_id), safe=""), updated)
    # SAS URLs are used only for this request; never attach the Store bearer.
    upload_client = transport
    status, _, _ = upload_client("PUT", upload_url, {"x-ms-blob-type": "BlockBlob", "Content-Type": "application/zip"}, _package_zip(release["msix_name"], package))
    if status < 200 or status >= 300:
        fail()
    client.write("POST", "/submissions/" + urllib.parse.quote(str(submission_id), safe="") + "/commit")
    deadline = time.monotonic() + max(0, poll_seconds)
    while True:
        status = _status_name(client, str(submission_id))
        if status == "Published":
            return "published"
        if status in ACCEPTED_STATUSES:
            if time.monotonic() >= deadline:
                return "submitted"
            time.sleep(min(2, max(0, deadline - time.monotonic())))
        elif time.monotonic() >= deadline:
            fail()
        else:
            time.sleep(min(2, max(0, deadline - time.monotonic())))


def _package_zip(filename, data):
    # Store expects a ZIP archive containing the one package named in JSON.
    import io
    out = io.BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_STORED) as zf:
        zf.writestr(filename, data)
    return out.getvalue()


def main(argv=None):
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    resolve = sub.add_parser("resolve")
    resolve.add_argument("--tag", required=True)
    resolve.add_argument("--output", required=True)
    prepare = sub.add_parser("prepare")
    prepare.add_argument("--release-metadata", required=True)
    prepare.add_argument("--state", required=True)
    prepare.add_argument("--output", required=True)
    prepare.add_argument("--source-sha", required=True)
    submit_parser = sub.add_parser("submit")
    submit_parser.add_argument("--release-metadata", required=True)
    submit_parser.add_argument("--state", required=True)
    submit_parser.add_argument("--package", required=True)
    submit_parser.add_argument("--receipt", required=True)
    submit_parser.add_argument("--source-sha", required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "resolve":
            result = resolve_release(args.tag)
            _write_json(args.output, result)
            print("resolved")
        elif args.command == "prepare":
            release = _json_file(args.release_metadata)
            runtime, state = _preflight(release, args.source_sha)
            _write_json(args.output, runtime)
            _write_json(args.state, state)
            print("already_published" if state.get("status") == "already_published" else "prepared")
        else:
            release, state = _json_file(args.release_metadata), _json_file(args.state)
            print(submit(release, state, args.package, args.receipt, args.source_sha))
        return 0
    except ControllerError as error:
        print("store_update_" + error.code, file=sys.stderr)
        return 1
    except Exception:
        print("store_update_failed", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
