# Code Review Task List

## `README.md`

- [x] Clarify `relay-target-delay` semantics.
- [x] If the current behavior is intentional, describe it as an initial join delay / prebuffer rather than a continuously maintained playback delay.
- [x] Document that buffered data may be consumed during upstream interruptions and the effective delay can shrink toward live edge.

## `cmd/root.go`

- [x] Consider renaming or rewording the `--relay-target-delay` flag description if it is only an initial subscriber start offset.
- [x] Consider adding a separate option later if a continuously maintained delay is desired.

## `pkg/config/config.go`

- [x] Keep `RelayTargetDelay` if it represents initial join delay, or rename it if the public configuration should avoid implying maintained playback delay.

## `pkg/server/relay_session.go`

- [x] Decide whether subscriber delivery should remain immediate after the initial delayed start, or whether chunks should be paced by `receivedAt` to maintain a target delay while upstream is healthy.
- [x] If maintaining delay is desired, pace `RelaySubscription.NextChunk` or `relayStream` so subscribers do not drain the initial backlog immediately.
- [x] Handle slow-subscriber underruns explicitly instead of silently continuing from the oldest retained chunk.
- [x] Review EOF handling for finite `.ts` streams; current behavior treats EOF as an unexpected upstream failure and reconnects indefinitely.

## `pkg/server/relay_buffer.go`

- [x] Review `chunkAtOrAfter` behavior when the requested sequence has already been trimmed.
- [x] Consider returning an explicit underrun/missed-data signal instead of returning the oldest available chunk.
- [x] Consider whether relay start positions need MPEG-TS packet alignment or stream-aware boundaries instead of arbitrary HTTP read chunks.

## `pkg/server/relay_manager.go`

- [x] Revisit relay eligibility so finite VOD-like `.ts` tracks are not accidentally treated as live relay streams.
- [x] Decide whether request headers should be part of the relay session key, since this reduces upstream sharing across clients with different headers.
- [x] If stronger sharing is desired, narrow the header fingerprint to only headers that materially affect upstream authorization or stream selection.

## `pkg/server/handlers.go`

- [x] Strip or normalize response headers that are unsafe for relayed streams, such as `Content-Length`, `Content-Range`, `Accept-Ranges`, and hop-by-hop headers.
- [x] If maintained target delay is desired, implement write pacing here or delegate paced reads to the subscription layer.

## `pkg/server/relay_buffer_test.go`

- [x] Add tests for trimmed-sequence underrun behavior.
- [x] Add tests clarifying whether `startSeqForDelay` represents initial join delay only or maintained playback delay.

## `pkg/server/relay_session_test.go`

- [x] Add a test proving a new subscriber either catches up immediately by design or remains paced behind live by the configured delay.
- [x] Add a test for slow subscribers when their requested sequence falls out of the retained buffer.
- [x] Add a test for normal upstream EOF behavior if finite `.ts` streams remain eligible or become explicitly ineligible.

## `pkg/server/relay_manager_test.go`

- [x] Add eligibility tests for `.ts` tracks that are likely VOD or finite assets, if there is metadata available to distinguish them.
- [x] Add session-key tests showing whether different `User-Agent` / `Accept` headers should or should not split relay sessions.
