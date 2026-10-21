# Traffic map: React Flow and embedded ELK

The frontend renders the existing read-only topology API with React Flow. ELK
is bundled in the image and runs in a same-origin browser Web Worker. No layout
service, Java runtime, external CDN or extra production container is required.

## Overview and grouping

- Server IP is the default aggregation. The overview shows four groups per
  page; active groups are listed first. Search, filters and the existing table
  still cover the observed projection (up to 60 graph routes, 500 table rows).
- Routes sharing an observed address belong to one group. Overlapping DNS
  address sets form a connected component. A common hostname or unknown DNS
  alone never merges independent profiles.
- Groups can span subscriptions and workers. Each member's counters contribute
  once. Expanding a group reveals its profiles inside the parent, including
  the profile's actual engine version in the details. Containment is not an
  observed network hop.
- One common server IP is a conservative capacity hint, not proof of a common
  backend, independent bandwidth, or public exit IP.

## Interaction and data fidelity

Pan, zoom, fit-to-bounds, minimap, keyboard selection and route focus are local
presentation operations. They never modify routing policies. Counters can
update without moving nodes or resetting the viewport. Only structural
changes, expansion or explicit rearrangement run ELK again. Dragged positions
remain stable across telemetry updates. Positions can be pinned.

The detail view links to recent recorded decisions for any member of an IP
group. It shows the selector's saved reason, actually compared candidates,
scores and IP-diversity factors. A current byte counter or readiness status is
never presented as the cause of an earlier selection. Missing decisions and
unknown metrics are explicitly labelled. Existing historical snapshots,
observed state changes, disconnect/reconnect, sampling and truncation warnings
remain available.

ELK is lazy-loaded on first layout. A usable grid is displayed during the cold
load. Worker initialization has a 30-second deadline, separate from the
8-second layout computation deadline. Failure gives a visible fallback grid,
and the user can retry. Worker lifetime ends when
the map is unmounted. Same-origin CSP applies to the document and the worker.
Static dependency notices ship with the frontend, including elkjs upstream
licensing. The rendered card UI uses the existing Material UI theme.

## Release gates

1. Build and TypeScript validation on the build host, with dependency versions locked.
2. Browser coverage of overlapping IP sets, unknown DNS separation, profile
   expansion/collapse, recorded reasons, pagination/search, keyboard/mobile,
   bounded large catalog projection, real worker lifecycle and fallback.
3. Inject successive SSE frames and prove positions, viewport and layout-count
   remain unchanged when only counters update. Record elapsed initial layout
   and full-page heap with the test size; do not call it isolated ELK memory.
4. Existing client-access browser checks and live lab API/reconnect smoke.
5. Back up the active production Compose, environment and frontend image;
   update only frontend; verify UI assets, worker script, authenticated map,
   and unchanged proxy/control container identities. Keep frontend-only rollback.

An illustrative mockup is not a framework performance measurement. Synthetic
browser results do not establish WAN throughput or relay-runtime performance.

## Fullscreen and readability

The map toolbar keeps Live/pause, collapse/expand filters and fullscreen visible.
Collapsing filters retains their values and shows the number of active criteria.
Native browser fullscreen includes the filters, graph and inspector; menus and
popovers remain within that fullscreen subtree. Unsupported/embedded browsers
fall back to a full-window overlay. Escape or the exit button restores the page;
unmount also restores body scrolling. Reduced-motion preferences disable the
filter transition.

Desktop cards are 480 by 164px (contained profiles 456 by 164px), with 16px titles. Metadata is on the left and compact speed/RTT plots are on the right. Mobile cards use340 by252px and place both plots side by side below the metadata. Initial layout uses 100% zoom rather than shrinking text to fit a large
graph. Pan/minimap navigate the overview; Fit explicitly shows all nodes, and
100% restores readable size around the current viewport center. Live counters,
filter collapse and fullscreen changes preserve the chosen viewport.

## Card speed and RTT history

Each card includes two small SVG plots, with no extra chart library. Speed uses
observed byte-counter deltas in Mbit/s and the selected smoothing window; upload
and download have separate colors/arrow legends. RTT is the runtime's smoothed
connection-delay estimate in milliseconds, not client ping. Repeated RTT values
are observations of the reported estimate, not a new active probe every frame.
Groups sum unique worker/epoch/engine rates and show the median available RTT.
Cross-worker observations align only within two seconds; incomplete sums stay
unknown. Client/input/config-only nodes have explicit unavailable telemetry
panels, never fabricated attribution.

History is in memory while the map is open: at most65 frames per worker epoch,
plots show at most the last60seconds. Initial speed requires two observations.
Counter resets, generation changes and frame gaps over15seconds interrupt rate
series; missing RTT is not zero. Epoch changes start a separate history. Plots
are local, obey direction filters and remain stable under pause/reconnect; no
extra probes, backend storage or ELK layout runs are introduced by chart updates.

The compact desktop overview uses12px vertical gaps: four collapsed IP groups span692px at100percent zoom. Fullscreen fit was checked at1920x1080 and3592x1956.
