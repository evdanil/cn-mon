#!/usr/bin/env python3
# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0
"""Offline unit tests for the Store update controller."""
import copy
import contextlib
import email.message
import hashlib
import importlib.util
import io
import json
import pathlib
import tempfile
import unittest
import zipfile
from unittest.mock import patch
from urllib.error import HTTPError

MODULE_PATH = pathlib.Path(__file__).with_name("store-update.py")
spec = importlib.util.spec_from_file_location("store_update", MODULE_PATH)
store_update = importlib.util.module_from_spec(spec)
spec.loader.exec_module(store_update)


class VersionTests(unittest.TestCase):
    def test_next_version_above_floor(self):
        self.assertEqual(store_update.next_package_version(["1.3.2.0"]), "1.3.3.0")

    def test_version_carries(self):
        self.assertEqual(store_update.next_package_version(["1.3.65535.0"]), "1.4.0.0")
        self.assertEqual(store_update.next_package_version(["1.65535.65535.0"]), "2.0.0.0")

    def test_bad_versions_fail_closed(self):
        for value in ("1.2.3", "1.2.3.4.5", "0.0.0.0", "1.2.3.1", "65536.0.0.0", "x.0.0.0"):
            with self.subTest(value=value), self.assertRaises(store_update.ControllerError):
                store_update.next_package_version([value])

    def test_exhaustion_fails(self):
        with self.assertRaises(store_update.ControllerError):
            store_update.next_package_version(["65535.65535.65535.0"])


class SubmissionTransformTests(unittest.TestCase):
    def setUp(self):
        self.published = {
            "id": "published-id", "status": "Published", "targetPublishMode": "Manual",
            "applicationPackages": [{"fileName": "app_x64.msix", "architecture": "X64", "fileStatus": "Uploaded", "version": "1.3.2.0"}],
            "listings": {"en-us": {"description": "keep", "assets": ["a"]}},
            "pricing": {"basePrice": "Free"}, "visibility": "Public",
            "packageDeliveryOptions": {"allowOffline": True},
            "unknownFutureField": {"nested": [1, {"x": "keep"}]},
            "notesForCertification": "Owner note",
        }
        self.release = {
            "release_id": 123, "release_tag": "v0.4.0", "release_commit": "a" * 40,
            "archive_sha256": "b" * 64, "packaging_revision": "c" * 40,
            "package_version": "1.3.3.0", "msix_name": "cn-mon_1.3.3.0_x64.msix",
        }

    def test_store_metadata_survives_package_replacement(self):
        original = copy.deepcopy(self.published)
        updated = store_update.prepare_submission(self.published, self.release)
        for field in ("listings", "pricing", "visibility", "packageDeliveryOptions", "unknownFutureField"):
            self.assertEqual(updated[field], original[field])
        self.assertEqual(updated["targetPublishMode"], "Immediate")
        self.assertEqual(updated["applicationPackages"][0]["fileStatus"], "PendingDelete")
        self.assertEqual(updated["applicationPackages"][-1]["fileStatus"], "PendingUpload")
        self.assertEqual(updated["applicationPackages"][-1]["minimumDirectXVersion"], "None")
        self.assertEqual(updated["applicationPackages"][-1]["minimumSystemRam"], "None")
        self.assertEqual(self.published, original)
        self.assertIn("Owner note", updated["notesForCertification"])
        self.assertIn('"schema":1', updated["notesForCertification"])

    def test_unsupported_architecture_is_rejected(self):
        self.published["applicationPackages"].append({"architecture": "ARM64", "fileName": "arm.msix", "fileStatus": "Uploaded", "version": "1.3.2.0"})
        with self.assertRaises(store_update.ControllerError):
            store_update.prepare_submission(self.published, self.release)

    def test_package_name_tracks_version(self):
        self.release["msix_name"] = "wrong.msix"
        with self.assertRaises(store_update.ControllerError):
            store_update.prepare_submission(self.published, self.release)

    def test_duplicate_marker_replaced_once(self):
        old = {"schema": 1, "release_id": 8, "tag": "v0.3.0", "commit": "a" * 40,
               "archive_sha256": "b" * 64, "packaging_revision": "c" * 40, "package_version": "1.3.2.0"}
        self.published["notesForCertification"] += "\nCNMON_STORE_UPDATE:" + json.dumps(old, separators=(",", ":"))
        updated = store_update.prepare_submission(self.published, self.release)
        self.assertEqual(updated["notesForCertification"].count("CNMON_STORE_UPDATE:"), 1)

    def test_notes_preserved_byte_for_byte_except_old_marker(self):
        old = {"schema": 1, "release_id": 8, "tag": "v0.3.0", "commit": "a" * 40,
               "archive_sha256": "b" * 64, "packaging_revision": "c" * 40, "package_version": "1.3.2.0"}
        self.published["notesForCertification"] = "Owner note  \r\nsecond line\r\nCNMON_STORE_UPDATE:" + json.dumps(old, separators=(",", ":")) + "\r\n"
        updated = store_update.prepare_submission(self.published, self.release)
        self.assertTrue(updated["notesForCertification"].startswith("Owner note  \r\nsecond line\r\n"))


def github_fixture(tag="v0.4.0", compare="behind", draft=False, prerelease=False, annotated=False, unsafe_entry=None):
    version = tag[1:]
    root = f"cn-mon_{version}_windows_amd64"
    win_name = root + ".zip"
    linux_name = f"cn-mon_{version}_linux_amd64.tar.gz"
    archive = io.BytesIO()
    binary = b"trusted binary"
    with zipfile.ZipFile(archive, "w") as zf:
        for name, payload in (("cn-mon.exe", binary), ("README.md", b"readme"), ("LICENSE", b"license"), ("THIRD_PARTY_NOTICES.md", b"notices")):
            zf.writestr(root + "/" + name, payload)
        if unsafe_entry:
            zf.writestr(root + "/" + unsafe_entry, b"unsafe")
    win_data = archive.getvalue()
    linux_data = b"linux archive"
    sums = (hashlib.sha256(win_data).hexdigest() + "  " + win_name + "\n" +
            hashlib.sha256(linux_data).hexdigest() + "  " + linux_name + "\n").encode()
    blobs = {win_name: win_data, linux_name: linux_data, "SHA256SUMS": sums}
    assets = []
    for index, (name, payload) in enumerate(blobs.items(), 1):
        assets.append({"id": index, "name": name, "digest": "sha256:" + hashlib.sha256(payload).hexdigest(),
                       "browser_download_url": f"https://github.com/evdanil/cn-mon/releases/download/{tag}/{name}"})
    commit = "a" * 40
    release = {"id": 9, "draft": draft, "prerelease": prerelease, "tag_name": tag, "assets": assets}
    tag_object = {"type": "tag", "sha": "e" * 40} if annotated else {"type": "commit", "sha": commit}
    def transport(method, url, headers=None, body=None):
        if url.endswith("/releases/tags/" + tag):
            obj = release
        elif url.endswith("/git/ref/tags/" + tag):
            obj = {"object": tag_object}
        elif url.endswith("/git/tags/" + "e" * 40):
            obj = {"object": {"type": "commit", "sha": commit}}
        elif url.endswith("/compare/main..." + commit):
            obj = {"status": compare}
        elif "/releases/download/" in url:
            obj = blobs[url.rsplit("/", 1)[-1]]
            return 200, obj, {}
        else:
            raise AssertionError(url)
        return 200, json.dumps(obj).encode(), {}
    return transport, blobs, release


STORE_ENV = {
    "CNMON_STORE_APP_ID": "store-app-id",
    "CNMON_STORE_TENANT_ID": "tenant-id",
    "CNMON_STORE_CLIENT_ID": "client-id",
    "CNMON_STORE_CLIENT_SECRET": "client-secret-poison",
    "CNMON_MSIX_IDENTITY_NAME": "Example.CnMon",
    "CNMON_MSIX_IDENTITY_PUBLISHER": "CN=Example Publisher",
    "CNMON_MSIX_PUBLISHER_DISPLAY_NAME": "Example Publisher",
    "CNMON_MSIX_EXPECTED_PFN": "Example.CnMon_123456789abcd",
}


def store_fixture(pending=False, statuses=None, fail_at=None, change_published_after_first=False,
                  pricing=None, clone_pricing=None):
    published = {
        "id": "published-id", "status": "Published", "targetPublishMode": "Manual",
        "applicationPackages": [{"fileName": "cn-mon_1.3.2.0_x64.msix", "fileStatus": "Uploaded", "version": "1.3.2.0", "architecture": "X64"}],
        "notesForCertification": "owner notes", "pricing": copy.deepcopy(pricing if pricing is not None else {"priceId": "Free", "futurePricingField": {"keep": True}}), "listings": {"en-us": {"title": "kept"}},
        "packageDeliveryOptions": {"unknown": True}, "unknownFutureField": {"nested": [1, 2]},
    }
    app = {"packageIdentityName": STORE_ENV["CNMON_MSIX_IDENTITY_NAME"],
           "publisherName": STORE_ENV["CNMON_MSIX_IDENTITY_PUBLISHER"],
           "packageFamilyName": STORE_ENV["CNMON_MSIX_EXPECTED_PFN"],
           "lastPublishedApplicationSubmission": {"id": "published-id"},
           "pendingApplicationSubmission": {"id": "pending-id"} if pending else None}
    class Transport:
        def __init__(self):
            self.calls = []
            self.statuses = list(statuses or ["Published"])
            self.upload_headers = None
            self.upload_bytes = None
            self.published_reads = 0
        def __call__(self, method, url, headers=None, body=None):
            self.calls.append((method, url, dict(headers or {}), body))
            if url.endswith("/oauth2/token"):
                return 200, b'{"access_token":"token-value"}', {}
            if url.startswith(store_update.STORE):
                path = url[len(store_update.STORE):]
                if method == "GET" and path.endswith("/status"):
                    state = self.statuses.pop(0) if len(self.statuses) > 1 else self.statuses[0]
                    return 200, json.dumps({"status": state}).encode(), {}
                if method == "GET" and path.endswith("/applications/store-app-id"):
                    return 200, json.dumps(app).encode(), {}
                if method == "GET" and path.endswith("/submissions/published-id"):
                    self.published_reads += 1
                    current = copy.deepcopy(published)
                    if change_published_after_first and self.published_reads > 1:
                        current["unknownFutureField"] = {"changed": True}
                    return 200, json.dumps(current).encode(), {}
                if method == "POST" and path.endswith("/submissions"):
                    if fail_at == "create":
                        raise store_update.ControllerError("create timeout poison")
                    clone = copy.deepcopy(published)
                    if fail_at == "newer_clone":
                        clone["applicationPackages"][0]["version"] = "1.3.4.0"
                    if clone_pricing is not None:
                        clone["pricing"] = copy.deepcopy(clone_pricing)
                    clone.update({"id": "new-id", "status": "PendingCommit", "fileUploadUrl": "https://upload.blob.core.windows.net/container?sig=private"})
                    return 201, json.dumps(clone).encode(), {}
                if method == "PUT" and path.endswith("/submissions/new-id"):
                    if fail_at == "update":
                        raise store_update.ControllerError("update error poison")
                    self.updated = json.loads(body)
                    return 200, b"{}", {}
                if method == "POST" and path.endswith("/commit"):
                    if fail_at == "commit":
                        raise store_update.ControllerError("commit timeout poison")
                    return 202, b"{}", {}
                raise AssertionError((method, path))
            if method == "PUT" and ".blob.core.windows.net" in url:
                self.upload_headers = dict(headers or {})
                self.upload_bytes = body
                if fail_at == "upload":
                    raise store_update.ControllerError("upload error poison")
                return 201, b"", {}
            raise AssertionError((method, url))
    return Transport(), published


def make_candidate(tmpdir, release):
    binary = b"trusted binary"
    manifest = (f'''<Package xmlns="http://schemas.microsoft.com/appx/manifest/foundation/windows10" xmlns:uap="http://schemas.microsoft.com/appx/manifest/uap/windows10"><Identity Name="{STORE_ENV['CNMON_MSIX_IDENTITY_NAME']}" Publisher="{STORE_ENV['CNMON_MSIX_IDENTITY_PUBLISHER']}" Version="{release['package_version']}" ProcessorArchitecture="x64" /><Properties><DisplayName>cn-mon — Network Monitor</DisplayName><PublisherDisplayName>{STORE_ENV['CNMON_MSIX_PUBLISHER_DISPLAY_NAME']}</PublisherDisplayName></Properties><Applications><Application Id="CnMon"><uap:VisualElements DisplayName="cn-mon — Network Monitor" /></Application></Applications></Package>''').encode()
    package_path = pathlib.Path(tmpdir) / release["msix_name"]
    with zipfile.ZipFile(package_path, "w") as zf:
        package_files = {"cn-mon.exe": binary, "AppxManifest.xml": manifest, "README.md": b"readme", "LICENSE": b"license",
                         "THIRD_PARTY_NOTICES.md": b"notices", "cn-mon.exe.manifest": b"exe manifest", "README-MSIX.md": b"msix readme",
                         "Assets/StoreLogo.png": b"logo", "Assets/Square44x44Logo.png": b"small", "Assets/Square150x150Logo.png": b"large"}
        for name, payload in package_files.items():
            zf.writestr(name, payload)
    package_hash = hashlib.sha256(package_path.read_bytes()).hexdigest()
    receipt = {"schema_version": 1, "purpose": "candidate", "package_version": release["package_version"],
               "cli_version": release["cli_version"], "cli_revision": release["cli_revision"],
               "packaging_revision": release["packaging_revision"], "archive_sha256": release["archive_sha256"],
               "binary_sha256": release["binary_sha256"], "name": STORE_ENV["CNMON_MSIX_IDENTITY_NAME"],
               "publisher": STORE_ENV["CNMON_MSIX_IDENTITY_PUBLISHER"],
               "publisher_display_name": STORE_ENV["CNMON_MSIX_PUBLISHER_DISPLAY_NAME"],
               "expected_pfn": STORE_ENV["CNMON_MSIX_EXPECTED_PFN"], "architecture": "x64",
               "files": {name: (release["binary_sha256"] if name == "cn-mon.exe" else hashlib.sha256(payload).hexdigest()) for name, payload in package_files.items()},
               "manifest_sha256": hashlib.sha256(manifest).hexdigest(), "package_sha256": package_hash, "signing": "unsigned"}
    receipt_path = pathlib.Path(tmpdir) / "receipt.json"
    receipt_path.write_text(json.dumps(receipt), encoding="utf-8")
    return package_path, receipt_path


class ReleaseResolutionTests(unittest.TestCase):
    def test_resolves_stable_release_with_hashes_and_provenance(self):
        transport, blobs, _ = github_fixture()
        resolved = store_update.resolve_release("v0.4.0", transport)
        self.assertEqual(resolved["cli_version"], "0.4.0")
        self.assertEqual(resolved["release_commit"], "a" * 40)
        self.assertEqual(resolved["archive_sha256"], hashlib.sha256(blobs[resolved["archive_name"]]).hexdigest())
        self.assertEqual(resolved["binary_sha256"], hashlib.sha256(b"trusted binary").hexdigest())

    def test_rejects_noncanonical_and_nonstable_tags(self):
        transport, _, _ = github_fixture()
        for tag in ("0.4.0", "v0.4.0-rc.1", "v01.4.0"):
            with self.subTest(tag=tag), self.assertRaises(store_update.ControllerError):
                store_update.resolve_release(tag, transport)
        transport, _, _ = github_fixture(draft=True)
        with self.assertRaises(store_update.ControllerError):
            store_update.resolve_release("v0.4.0", transport)
        transport, _, _ = github_fixture(prerelease=True)
        with self.assertRaises(store_update.ControllerError):
            store_update.resolve_release("v0.4.0", transport)

    def test_requires_release_commit_to_be_ancestor_of_main(self):
        transport, _, _ = github_fixture(compare="ahead")
        with self.assertRaises(store_update.ControllerError):
            store_update.resolve_release("v0.4.0", transport)

    def test_peels_annotated_tag_to_commit(self):
        transport, _, _ = github_fixture(annotated=True)
        resolved = store_update.resolve_release("v0.4.0", transport)
        self.assertEqual(resolved["release_commit"], "a" * 40)

    def test_rejects_missing_duplicate_digest_and_unsafe_assets(self):
        for mode in ("missing", "duplicate", "digest", "unsafe"):
            with self.subTest(mode=mode):
                transport, blobs, release = github_fixture(unsafe_entry="../escape" if mode == "unsafe" else None)
                asset_name = next(a["name"] for a in release["assets"] if a["name"].endswith("windows_amd64.zip"))
                if mode == "missing":
                    release["assets"] = [a for a in release["assets"] if a["name"] != asset_name]
                elif mode == "duplicate":
                    release["assets"].append(copy.deepcopy(next(a for a in release["assets"] if a["name"] == asset_name)))
                elif mode == "digest":
                    next(a for a in release["assets"] if a["name"] == asset_name)["digest"] = "sha256:" + "0" * 64
                with self.assertRaises(store_update.ControllerError):
                    store_update.resolve_release("v0.4.0", transport)

    def test_rejects_duplicate_or_path_injected_checksum_entries(self):
        for injected in (b"0" * 64 + b"  ../escape\n", b"0" * 64 + b"  cn-mon_0.4.0_windows_amd64.zip\n"):
            with self.subTest(injected=injected):
                transport, blobs, release = github_fixture()
                name = "SHA256SUMS"
                blobs[name] += injected
                sums_asset = next(a for a in release["assets"] if a["name"] == name)
                sums_asset["digest"] = "sha256:" + hashlib.sha256(blobs[name]).hexdigest()
                with self.assertRaises(store_update.ControllerError):
                    store_update.resolve_release("v0.4.0", transport)

    def test_public_asset_redirect_is_narrowly_allowed_without_forwarded_auth(self):
        seen = []
        class Response:
            status = 200
            headers = {}
            def __enter__(self): return self
            def __exit__(self, *args): return False
            def read(self, size): return b"asset"
        class Opener:
            def open(self, req, timeout):
                seen.append((req.full_url, dict(req.header_items())))
                if len(seen) == 1:
                    headers = email.message.Message()
                    headers["Location"] = "https://release-assets.githubusercontent.com/file?sig=x"
                    error = HTTPError(req.full_url, 302, "redirect", headers, io.BytesIO())
                    error.close()
                    raise error
                return Response()
        with patch.object(store_update.urllib.request, "build_opener", return_value=Opener()):
            status, data, _ = store_update.request("GET", "https://github.com/evdanil/cn-mon/releases/download/v0.4.0/file.zip", {"Authorization": "Bearer private"})
        self.assertEqual((status, data), (200, b"asset"))
        self.assertEqual(len(seen), 2)
        self.assertNotIn("Authorization", seen[1][1])

    def test_public_asset_redirect_to_untrusted_host_is_rejected(self):
        class Opener:
            def open(self, req, timeout):
                headers = email.message.Message()
                headers["Location"] = "https://evil.example/steal"
                error = HTTPError(req.full_url, 302, "redirect", headers, io.BytesIO())
                error.close()
                raise error
        with patch.object(store_update.urllib.request, "build_opener", return_value=Opener()):
            with self.assertRaises(store_update.ControllerError):
                store_update.request("GET", "https://github.com/evdanil/cn-mon/releases/download/v0.4.0/file.zip")


class StoreTransactionTests(unittest.TestCase):
    def runtime_release(self, github_transport):
        release = store_update.resolve_release("v0.4.0", github_transport, "d" * 40)
        release.update({"package_version": "1.3.3.0", "msix_name": "cn-mon_1.3.3.0_x64.msix"})
        return release

    def state(self, release, published):
        return {"schema_version": 1, "status": "ready", "published_submission_id": "published-id",
                "published_submission_sha256": store_update._published_fingerprint(published), "package_version": release["package_version"],
                "release_binding": {k: release[k] for k in ("release_id", "release_tag", "release_commit", "archive_sha256", "packaging_revision")}}

    def invoke(self, statuses=None, pending=False, allow_failure=False, fail_at=None, changed_release=False,
               change_asset=False, change_published_after_first=False, receipt_case=None):
        github, _, live_release = github_fixture()
        release = self.runtime_release(github)
        transport, published = store_fixture(pending=pending, statuses=statuses, fail_at=fail_at,
                                             change_published_after_first=change_published_after_first)
        if changed_release:
            live_release["id"] += 1
        if change_asset:
            live_release["assets"][0]["digest"] = "sha256:" + "0" * 64
        with tempfile.TemporaryDirectory() as tmpdir, patch.dict("os.environ", STORE_ENV):
            package, receipt = make_candidate(tmpdir, release)
            if receipt_case:
                value = json.loads(receipt.read_text(encoding="utf-8"))
                if receipt_case == "package_bytes":
                    package.write_bytes(package.read_bytes() + b"tampered")
                elif receipt_case == "identity":
                    value["publisher"] = "CN=Wrong Publisher"
                    receipt.write_text(json.dumps(value), encoding="utf-8")
                elif receipt_case == "signature":
                    value["signing"] = {"status": "test"}
                    receipt.write_text(json.dumps(value), encoding="utf-8")
                elif receipt_case == "cli_version":
                    value["cli_version"] = "0.3.0"
                    receipt.write_text(json.dumps(value), encoding="utf-8")
                elif receipt_case == "title":
                    contents = {}
                    with zipfile.ZipFile(package) as zf:
                        contents = {info.filename: zf.read(info) for info in zf.infolist()}
                    contents["AppxManifest.xml"] = contents["AppxManifest.xml"].replace("cn-mon — Network Monitor".encode(), b"Wrong title")
                    encoded = io.BytesIO()
                    with zipfile.ZipFile(encoded, "w") as zf:
                        for name, data in contents.items():
                            zf.writestr(name, data)
                    package.write_bytes(encoded.getvalue())
                    manifest_hash = hashlib.sha256(contents["AppxManifest.xml"]).hexdigest()
                    value["files"]["AppxManifest.xml"] = manifest_hash
                    value["manifest_sha256"] = manifest_hash
                    value["package_sha256"] = hashlib.sha256(package.read_bytes()).hexdigest()
                    receipt.write_text(json.dumps(value), encoding="utf-8")
            try:
                result = store_update.submit(release, self.state(release, published), package, receipt, "d" * 40,
                                             transport=transport, github_client=github, poll_seconds=0)
            except store_update.ControllerError:
                if not allow_failure:
                    raise
                result = "failed"
        return result, transport

    def test_pending_submission_blocks_all_writes(self):
        result, transport = self.invoke(pending=True, allow_failure=True)
        self.assertEqual(result, "failed")
        # The only POST is OAuth; no Store submission mutation is attempted.
        self.assertFalse(any(url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE") for method, url, _, _ in transport.calls))

    def test_successful_flow_orders_create_update_upload_commit_poll(self):
        result, transport = self.invoke(statuses=["Published"])
        self.assertEqual(result, "published")
        store_calls = [(method, url, body) for method, url, _, body in transport.calls if url.startswith(store_update.STORE)]
        methods_paths = [(method, url.split("/my", 1)[-1]) for method, url, _ in store_calls]
        self.assertEqual([m for m, _ in methods_paths], ["GET", "GET", "GET", "GET", "POST", "PUT", "POST", "GET"])
        self.assertIsNone(store_calls[4][2])
        self.assertIsNone(store_calls[6][2])
        self.assertFalse(any(method == "DELETE" for method, _, _ in store_calls))
        self.assertNotIn("Authorization", transport.upload_headers)
        with zipfile.ZipFile(io.BytesIO(transport.upload_bytes)) as zf:
            self.assertEqual(zf.namelist(), ["cn-mon_1.3.3.0_x64.msix"])
        self.assertEqual(transport.updated["unknownFutureField"], {"nested": [1, 2]})

    def test_accepted_intermediate_commit_reports_submitted(self):
        result, _ = self.invoke(statuses=["Certification"])
        self.assertEqual(result, "submitted")

    def test_unknown_status_fails_without_claiming_success(self):
        with self.assertRaises(store_update.ControllerError):
            self.invoke(statuses=["UnexpectedStatus"])

    def test_commit_started_deadline_is_not_success(self):
        with self.assertRaises(store_update.ControllerError):
            self.invoke(statuses=["CommitStarted"])

    def test_changed_private_state_stops_before_any_store_write(self):
        github, _, _ = github_fixture()
        release = self.runtime_release(github)
        transport, published = store_fixture()
        bad_state = self.state(release, published)
        bad_state["published_submission_sha256"] = "0" * 64
        with tempfile.TemporaryDirectory() as tmpdir, patch.dict("os.environ", STORE_ENV):
            package, receipt = make_candidate(tmpdir, release)
            with self.assertRaises(store_update.ControllerError):
                store_update.submit(release, bad_state, package, receipt, "d" * 40,
                                    transport=transport, github_client=github, poll_seconds=0)
        self.assertFalse(any(url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE") for method, url, _, _ in transport.calls))

    def test_prepare_detects_exact_published_release_without_allocating_again(self):
        github, _, _ = github_fixture()
        release = store_update.resolve_release("v0.4.0", github)
        transport, published = store_fixture()
        marker_release = dict(release, packaging_revision="d" * 40, package_version="1.3.3.0")
        published["applicationPackages"][0]["version"] = "1.3.3.0"
        published["notesForCertification"] = "owner notes\n" + store_update.MARKER_PREFIX + json.dumps(store_update._marker(marker_release), separators=(",", ":"))
        with patch.dict("os.environ", STORE_ENV):
            runtime, state = store_update._preflight(release, "d" * 40, transport=transport)
        self.assertEqual(runtime["package_version"], "1.3.3.0")
        self.assertEqual(state["status"], "already_published")
        self.assertFalse(any(url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE") for method, url, _, _ in transport.calls))

    def test_intervening_release_change_stops_before_store_reads_or_writes(self):
        result, transport = self.invoke(allow_failure=True, changed_release=True)
        self.assertEqual(result, "failed")
        self.assertFalse(any(url.startswith(store_update.STORE) for _, url, _, _ in transport.calls))

    def test_intervening_release_asset_change_stops_before_store_reads_or_writes(self):
        result, transport = self.invoke(allow_failure=True, change_asset=True)
        self.assertEqual(result, "failed")
        self.assertFalse(any(url.startswith(store_update.STORE) for _, url, _, _ in transport.calls))

    def test_published_change_between_fresh_reads_stops_before_create(self):
        result, transport = self.invoke(allow_failure=True, change_published_after_first=True)
        self.assertEqual(result, "failed")
        self.assertEqual(transport.published_reads, 2)
        self.assertFalse(any(url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE") for method, url, _, _ in transport.calls))

    def test_each_write_boundary_failure_stops_and_is_never_retried(self):
        expected_stages = {"create": 1, "update": 2, "upload": 3, "commit": 4}
        for failure, stage_count in expected_stages.items():
            with self.subTest(failure=failure):
                result, transport = self.invoke(allow_failure=True, fail_at=failure)
                self.assertEqual(result, "failed")
                store_calls = [(method, url) for method, url, _, _ in transport.calls if url.startswith(store_update.STORE)]
                writes = [(method, url) for method, url, _, _ in transport.calls
                          if (url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE")) or
                          (".blob.core.windows.net" in url and method == "PUT")]
                self.assertEqual(len(writes), stage_count)
                self.assertFalse(any(method == "DELETE" for method, _ in writes))

    def test_newer_server_clone_stops_after_create_before_replacement(self):
        result, transport = self.invoke(allow_failure=True, fail_at="newer_clone")
        self.assertEqual(result, "failed")
        writes = [(method, url) for method, url, _, _ in transport.calls
                  if (url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE")) or
                  (".blob.core.windows.net" in url and method == "PUT")]
        self.assertEqual(len(writes), 1)
        self.assertTrue(writes[0][0] == "POST" and writes[0][1].endswith("/submissions"))

    def test_unsupported_published_pricing_stops_before_store_writes(self):
        invalid = ({"priceId": "Base"}, {"priceId": " Base "}, {}, {"priceId": None}, {"priceId": ""})
        for pricing in invalid:
            with self.subTest(pricing=pricing):
                github, _, _ = github_fixture()
                release = self.runtime_release(github)
                transport, published = store_fixture(pricing=pricing)
                state = self.state(release, published)
                with tempfile.TemporaryDirectory() as tmpdir, patch.dict("os.environ", STORE_ENV):
                    package, receipt = make_candidate(tmpdir, release)
                    with self.assertRaises(store_update.ControllerError) as raised:
                        store_update.submit(release, state, package, receipt, "d" * 40,
                                            transport=transport, github_client=github, poll_seconds=0)
                self.assertEqual(raised.exception.code, "unsupported_pricing")
                self.assertFalse(any(url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE")
                                     for method, url, _, _ in transport.calls))

    def test_supported_pricing_is_preserved_without_changes(self):
        for price_id in ("Free", "Tier", "CustomTier"):
            with self.subTest(price_id=price_id):
                pricing = {"priceId": price_id, "futurePricingField": {"keep": [1, "value"]}}
                github, _, _ = github_fixture()
                release = self.runtime_release(github)
                transport, published = store_fixture(pricing=pricing)
                state = self.state(release, published)
                with tempfile.TemporaryDirectory() as tmpdir, patch.dict("os.environ", STORE_ENV):
                    package, receipt = make_candidate(tmpdir, release)
                    store_update.submit(release, state, package, receipt, "d" * 40,
                                        transport=transport, github_client=github, poll_seconds=0)
                self.assertEqual(transport.updated["pricing"], pricing)

    def test_prepare_rejects_invalid_pricing_before_package_allocation(self):
        for pricing in ("missing", None, {}, {"priceId": "  "}, {"priceId": "bAsE"}):
            with self.subTest(pricing=pricing):
                github, _, _ = github_fixture()
                release = store_update.resolve_release("v0.4.0", github)
                transport, published = store_fixture()
                if pricing == "missing":
                    published.pop("pricing")
                else:
                    published["pricing"] = pricing
                with patch.dict("os.environ", STORE_ENV):
                    with self.assertRaises(store_update.ControllerError) as raised:
                        store_update._preflight(release, "d" * 40, transport=transport)
                self.assertEqual(raised.exception.code, "unsupported_pricing")
                self.assertFalse(any(url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE")
                                     for method, url, _, _ in transport.calls))

    def test_unsupported_pricing_on_created_clone_stops_before_replacement(self):
        github, _, _ = github_fixture()
        release = self.runtime_release(github)
        transport, published = store_fixture(clone_pricing={"priceId": " Base "})
        with tempfile.TemporaryDirectory() as tmpdir, patch.dict("os.environ", STORE_ENV):
            package, receipt = make_candidate(tmpdir, release)
            with self.assertRaises(store_update.ControllerError) as raised:
                store_update.submit(release, self.state(release, published), package, receipt, "d" * 40,
                                    transport=transport, github_client=github, poll_seconds=0)
        self.assertEqual(raised.exception.code, "unsupported_pricing")
        writes = [(method, url) for method, url, _, _ in transport.calls
                  if (url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE")) or
                  (".blob.core.windows.net" in url and method == "PUT")]
        self.assertEqual(len(writes), 1)
        self.assertEqual(writes[0][0], "POST")

    def test_already_published_submit_is_idempotent_and_has_zero_writes(self):
        github, _, _ = github_fixture()
        release = self.runtime_release(github)
        release["package_version"] = "1.3.3.0"
        release["msix_name"] = "cn-mon_1.3.3.0_x64.msix"
        transport, published = store_fixture()
        published["applicationPackages"][0]["version"] = "1.3.3.0"
        published["notesForCertification"] = "owner note\n" + store_update.MARKER_PREFIX + json.dumps(store_update._marker(release), separators=(",", ":"))
        state = self.state(release, published)
        state["status"] = "already_published"
        with tempfile.TemporaryDirectory() as tmpdir, patch.dict("os.environ", STORE_ENV):
            package, receipt = make_candidate(tmpdir, release)
            result = store_update.submit(release, state, package, receipt, "d" * 40,
                                         transport=transport, github_client=github, poll_seconds=0)
        self.assertEqual(result, "already_published")
        self.assertFalse(any(url.startswith(store_update.STORE) and method in ("POST", "PUT", "DELETE") for method, url, _, _ in transport.calls))

    def test_package_and_receipt_mutations_stop_before_store_api(self):
        for case in ("package_bytes", "identity", "signature", "cli_version", "title"):
            with self.subTest(case=case):
                result, transport = self.invoke(allow_failure=True, receipt_case=case)
                self.assertEqual(result, "failed")
                self.assertFalse(any(url.startswith(store_update.STORE) for _, url, _, _ in transport.calls))

    def test_cli_sanitizes_poisoned_internal_error(self):
        poison = "client-secret-poison-and-raw-store-body"
        with tempfile.TemporaryDirectory() as tmpdir:
            metadata = pathlib.Path(tmpdir) / "release.json"
            metadata.write_text("{}", encoding="utf-8")
            output = pathlib.Path(tmpdir) / "runtime.json"
            state = pathlib.Path(tmpdir) / "private.json"
            stdout, stderr = io.StringIO(), io.StringIO()
            with patch.object(store_update, "_preflight", side_effect=RuntimeError(poison)), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                code = store_update.main(["prepare", "--release-metadata", str(metadata), "--state", str(state), "--output", str(output), "--source-sha", "d" * 40])
            self.assertEqual(code, 1)
            self.assertNotIn(poison, stdout.getvalue() + stderr.getvalue())
            self.assertEqual(stderr.getvalue().strip(), "store_update_failed")

    def test_cli_emits_only_allowlisted_private_preflight_statuses(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            metadata = pathlib.Path(tmpdir) / "release.json"
            metadata.write_text("{}", encoding="utf-8")
            output = pathlib.Path(tmpdir) / "runtime.json"
            state = pathlib.Path(tmpdir) / "private.json"
            for code in ("missing_credentials", "pending_submission", "no_published_baseline", "published_state_changed"):
                stdout, stderr = io.StringIO(), io.StringIO()
                with self.subTest(code=code), patch.object(store_update, "_preflight", side_effect=store_update.ControllerError(code)), contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
                    result = store_update.main(["prepare", "--release-metadata", str(metadata), "--state", str(state), "--output", str(output), "--source-sha", "d" * 40])
                self.assertEqual(result, 1)
                self.assertEqual(stderr.getvalue().strip(), "store_update_" + code)
                self.assertNotIn(STORE_ENV["CNMON_STORE_CLIENT_SECRET"], stderr.getvalue() + stdout.getvalue())


if __name__ == "__main__":
    unittest.main()
