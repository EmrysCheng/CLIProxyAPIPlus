# Strict source overlays

The source trees in upstream/ are immutable. changes.json pins each preimage SHA-256 and every replacement count. files/ adds the public embedded facade and the CPA-only transport injection inside the original module. scripts/project.py prepares isolated copies and refuses drift rather than using fuzzy patch matching.

Embedded and standalone Manager entrypoints share lifecycle initialization, database locking, background-task drain and native maintenance paths. Their engineering tests do not replace the separate release/distribution and production-deployment acceptance checklists. No proxy protocol translator is modified.

## Reviewed upstream preimages

`changes.json` accepts either one lowercase SHA-256 string or a non-empty,
duplicate-free array of explicit full-file SHA-256 strings for a change. Arrays
are only for reviewed upstream revisions that use **identical** operations; all
replacement counts and the validate-all-before-write rule remain mandatory.
Unknown file contents are rejected even if every replacement string still exists.
This is not automatic hash approval or fuzzy patching.

For CLIProxyAPI v8.0.55 (`e6e5708e677adb2463d45accdd24bc9fd028ddd8`),
reviewed preimages were added for `internal/config/parse.go`,
`internal/config/config_load.go`, and `sdk/cliproxy/service_config.go`.
The upstream KV-store normalization/reload changes are retained; the existing
embedded configuration policy and scoped logging-default hooks are unchanged.
The original v8.0.53/v8.0.54 preimages remain supported. Unknown future versions
still require compatibility validation and possibly a new integration recipe.


## Heartbeat fixture timing contract

The unchanged v8.0.53/v8.0.55 SDK fixture preimage
`41a268211d4902524b90a0ab7d650e08b65437b231b330e5e23d8e1e081d534b`
contains a one-second replacement-subscription wait, although heartbeat detection
precedes an initial equal-jitter retry of 0.5 to 1 second. Isolated unadapted runs
produced 18 passes and 2 failures in 20 attempts. The explicit test-only overlay
uses the same `3 * homeSubscriberRetryInitial` bound as peer lifetime tests and
adds no-drain observation throughout the wait and after ACK. All original
registry, ACK, cancellation and blocking-scope assertions remain. No runtime
retry, cancellation bound, Home behavior or original snapshot is changed; no
test is skipped. Repeat and delayed-bad-drain mutation evidence is required.

## Response interrupt fixture timing contract

The unchanged v8.0.55 OpenAI responses interrupt fixture preimage
`45b7d4a949dac4c04657176661b011606c5b0a6e1eb07e231e2ae02945f27f04`
contains an immediate assertion on `executor.canceled.Load()` following
client receipt of `response.incomplete`. Upstream stream goroutine cancellation
via `<-ctx.Done()` and its `e.canceled.Store(true)` occurs asynchronously to
the downstream websocket notification. The explicit test-only overlay uses a
bounded 2-second polling wait before asserting upstream cancellation. No runtime
logic or cancellation assertion is relaxed; no test is skipped.
