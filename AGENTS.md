# AGENTS.md

## Working Style

- State assumptions explicitly before coding when behavior is ambiguous. If multiple interpretations are possible, call them out instead of picking silently.
- Prefer the smallest change that solves the verified problem. Do not add configurability, abstractions, or speculative handling that the request does not require.
- Keep edits surgical: touch only files and lines needed for the task, match the existing style, and do not clean up unrelated code while you are there.
- Remove only the unused code your own change creates. If you notice unrelated dead code or problems, mention them separately instead of folding them into the edit.
- Define success in verifiable terms before changing code. For bug fixes, reproduce with a test or other concrete check first when practical, then verify the fix with the narrowest useful command.
- For multi-step work, keep a brief goal-driven plan with a verification check for each step.

## Verify Changes

- Default local verification: `go test ./...`
- CI parity check: `go test -mod vendor -v -race ./... && go build -mod vendor`
- Docker verification: `docker build . --file Dockerfile --tag iptv-proxy:ci-build`
- For focused relay work, run `go test -v ./pkg/server/...`

## Entry Points

- CLI entrypoint is `main.go` -> `cmd.Execute()`.
- Most runtime wiring happens in `cmd/root.go`: Cobra/Viper flags, env loading, Xtream auto-detection, and `config.ProxyConfig` assembly.
- Server startup is `pkg/server.NewServer()` + `(*Config).Serve()`.
- Playlist loading/filtering and route generation happen before Gin starts; `--list-groups` still builds the server and loads playlists, it just exits before `Serve()`.

## Repo Structure

- `cmd/`: CLI flags, config/env parsing, source resolution.
- `pkg/server/`: main application logic: playlist loading, URL rewriting, Gin routes, streaming, relay buffering.
- `pkg/config/`: shared config types and a few global toggles used by server code.
- `pkg/xtream-proxy/`: thin package; most Xtream handling logic actually lives in `pkg/server/xtreamHandles.go`.

## Config And Env Gotchas

- Config file name defaults to `.iptv-proxy` in `$HOME` or the current directory; env vars are read through Viper with `-` replaced by `_`.
- Multi-value env vars like `M3U_SOURCE` and `INCLUDE_GROUP` use `|` as a separator, with `\|` escape support implemented in `cmd/root.go`.
- `--m3u-source` is the real multi-source path; `--m3u-url` is legacy single-source fallback.

## Relay-Specific Notes

- Relay behavior is implemented in `pkg/server/relay_manager.go`, `relay_session.go`, and `relay_buffer.go`.
- Current relay is only for eligible non-HLS TS-style live streams; bypass logic lives in `relayEligibility()`.
- `Range` handling is important for debugging player behavior. Check `request relay` / `request bypass` logs before assuming two viewers actually shared one session.
- Relay log format in code is the source of truth; README examples can lag behind current event names.

## Testing Notes

- Relay, playlist loading, and URL rewriting tests are all in `pkg/server/*_test.go`; that package is the highest-value place to add focused tests.
- CI runs with vendored deps and `-race`, so changes that pass plain `go test ./...` should still be checked with the CI-parity command before finishing substantial work.

## Build / Packaging Quirks

- CI and release workflows target Go 1.17.
- The repo includes `vendor/`, and CI explicitly uses it.
- `Dockerfile` builds with `GO111MODULE=off`; do not assume Docker build behavior matches normal module-based local commands.
