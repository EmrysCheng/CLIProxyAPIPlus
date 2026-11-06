# Third-party notices

CLIProxyAPIPlus incorporates source from:

- CLIProxyAPI, router-for-me/CLIProxyAPI, MIT. Complete copyright and permission notice: upstream/cliproxyapi/LICENSE; distributed as licenses/cliproxyapi-LICENSE.txt.
- CPA-Manager-Plus, seakee/CPA-Manager-Plus, MIT. Complete copyright and permission notice: upstream/cpa-manager-plus/LICENSE; distributed as licenses/cpa-manager-plus-LICENSE.txt.

Immutable source snapshots retain their original notices. Generated build artifacts include SPDX 2.3 metadata, dependency notice copies when available, a hashed notice inventory and a compliance report. The inventory conservatively includes the resolved Go module graph and npm lock entries, including development/build dependencies; it is not an exact binary reachability analysis.

License declarations are upstream package metadata, not a legal conclusion. NOASSERTION and missing notice files are explicitly reported. Legal review, attribution completeness, vulnerability review and any distribution restrictions remain publication gates. Checksums do not prove provenance, and a local build is not a production deployment approval. No license is assigned here to newly written product code on behalf of its owner.


## Bundled build SDKs (0.4.0)

Portable packages redistribute pinned Go and Node.js official SDK archives, and Astral python-build-standalone CPython distributions. The Node archive includes npm and its dependencies. Original SDK license/notice files are retained unmodified under `build-tools/`; Python's publisher build-repository license texts are additionally retained under `build-tools/python-licenses/`. See `licenses/BUNDLED_TOOLS_CN.md`, `build-tools/toolchain-manifest.json`, and `updater-source/internal/sourceupdate/toolchains.lock.json` for versions, origins and checksums. This inventory is not a claim of a completed legal or security audit. Linux SDKs require glibc; they are not musl/Alpine distributions.
