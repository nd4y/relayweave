# Third-party components

RelayWeave starts unmodified Xray-core release binaries as separate processes. Xray-core uses MPL-2.0. The builtin directory retains the upstream release LICENSE, as do downloaded cache artifacts when supplied by their archives.

- [Xray26.10.17 release](https://github.com/XTLS/Xray-core/releases/tag/v26.10.17)
- [Xray26.9.30 compatibility fixture](https://github.com/XTLS/Xray-core/releases/tag/v26.9.30)
- [Upstream source and license](https://github.com/XTLS/Xray-core)

Exact platform archive/binary digests are in artifacts.lock.json. Custom variants must supply repository/tag/license metadata; approval does not apply that license to the application's original source.

React, Material UI, MUI X Community Data Grid and Emotion retain their upstream licenses. Go/frontend dependency versions are recorded in go.mod/go.sum and web/package-lock.json. PostgreSQL, nginx, Prometheus, Grafana, Loki, Go, Node.js and distroless images retain their upstream licenses/notices; preserve those when redistributing their artifacts.

The frontend build preserves installed production dependency notices at `/third-party-notices.txt`. The runtime image contains imported Go module and compiler notices in `/usr/share/relayweave/licenses/`. The exact MUI X virtualizer0.4.2 manifest declares MIT but its npm/source directory omits a notice file; the generated bundle explicitly identifies the shared MUI X Community MIT notice from x-data-grid8.29.5 as its notice source. Updating that exception requires review.

This notice documents third-party provenance and does not grant a new license to original RelayWeave source.
