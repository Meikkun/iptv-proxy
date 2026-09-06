# Iptv Proxy

[![Actions Status](https://github.com/pierre-emmanuelJ/iptv-proxy/workflows/CI/badge.svg)](https://github.com/pierre-emmanuelJ/iptv-proxy/actions?query=workflow%3ACI)

NOTE: This fork of the [original project](https://github.com/pierre-emmanuelJ/iptv-proxy) contains the following enhancements:

- Corrected issue with Xtream Codes EPG not loading
- Fixed issue with Xtream Codes VOD (Shows & Movies) as the IPTV provider returned data seems to be partially complete, or missing pieces that this proxy is expecting



## Description

Iptv-Proxy is a project to proxyfie an m3u file
and to proxyfie an Xtream iptv service (client API).

### M3U and M3U8

M3U service convert an iptv m3u file into a web proxy server.

It's transform all the original tracks to an new url pointing on the proxy.


### Xtream code client api

proxy on Xtream code (client API)

support live, vod, series and full epg :rocket:

### M3u Example

Original iptv m3u file

```m3u
#EXTM3U
#EXTINF:-1 tvg-ID="examplechanel1.com" tvg-name="chanel1" tvg-logo="http://ch.xyz/logo1.png" group-title="USA HD",CHANEL1-HD
http://iptvexample.net:1234/12/test/1
#EXTINF:-1 tvg-ID="examplechanel2.com" tvg-name="chanel2" tvg-logo="http://ch.xyz/logo2.png" group-title="USA HD",CHANEL2-HD
http://iptvexample.net:1234/13/test/2
#EXTINF:-1 tvg-ID="examplechanel3.com" tvg-name="chanel3" tvg-logo="http://ch.xyz/logo3.png" group-title="USA HD",CHANEL3-HD
http://iptvexample.net:1234/14/test/3
#EXTINF:-1 tvg-ID="examplechanel4.com" tvg-name="chanel4" tvg-logo="http://ch.xyz/logo4.png" group-title="USA HD",CHANEL4-HD
http://iptvexample.net:1234/15/test/4
```

What M3U proxy IPTV do
 - convert chanels url to new endpoints
 - convert original m3u file with new routes pointing to the proxy
 - can merge multiple M3U sources into a single output playlist
 - can keep only selected `group-title` categories in the generated playlist

Start proxy server example

```Bash
iptv-proxy --m3u-url http://example.com/get.php?username=user&password=pass&type=m3u_plus&output=m3u8 \
             --port 8080 \
             --hostname proxyexample.com \
             --user test \
             --password passwordtest
```


 That's give you an m3u file on a specific endpoint `iptv.m3u` in our example
 
 `http://proxyserver.com:8080/iptv.m3u?username=test&password=passwordtest`

All the new routes pointing on your proxy server
```m3u
#EXTM3U
#EXTINF:-1 tvg-ID="examplechanel1.com" tvg-name="chanel1" tvg-logo="http://ch.xyz/logo1.png" group-title="USA HD",CHANEL1-HD
http://proxyserver.com:8080/12/test/1?username=test&password=passwordtest
#EXTINF:-1 tvg-ID="examplechanel2.com" tvg-name="chanel2" tvg-logo="http://ch.xyz/logo2.png" group-title="USA HD",CHANEL2-HD
http://proxyserver.com:8080/13/test/2?username=test&password=passwordtest
#EXTINF:-1 tvg-ID="examplechanel3.com" tvg-name="chanel3" tvg-logo="http://ch.xyz/logo3.png" group-title="USA HD",CHANEL3-HD
http://proxyserver.com:8080/14/test/3?username=test&password=passwordtest
#EXTINF:-1 tvg-ID="examplechanel4.com" tvg-name="chanel4" tvg-logo="http://ch.xyz/logo4.png" group-title="USA HD",CHANEL4-HD
http://proxyserver.com:8080/15/test/4?username=test&password=passwordtest
```

### Multiple M3U sources and group filtering

The proxy can merge multiple input playlists and preserve their original
`group-title` values in the final output.

Example:

```Bash
iptv-proxy --m3u-source http://provider-a.example/list.m3u \
             --m3u-source http://provider-b.example/list.m3u \
             --include-group Sports \
             --include-group News \
             --port 8080 \
             --hostname proxyexample.com \
             --user test \
             --password passwordtest
```

Useful options:

- `--m3u-source` repeatable M3U source input
- `--m3u-url` legacy single-source option
- `--include-group` repeatable `group-title` filter, supporting exact matches and `*`/`?` wildcards
- `--list-groups` prints discovered groups/categories and exits

Environment variables use `|` as a separator:

```Shell
M3U_SOURCE="http://provider-a.example/list.m3u|http://provider-b.example/list.m3u"
INCLUDE_GROUP="Sports|News"
```

Wildcard examples:

```Shell
INCLUDE_GROUP='Sports*|News*'
iptv-proxy --include-group 'Sports*' --include-group 'Movies ?D'
```

Wildcard filters keep the original `group-title` values in the generated playlist.

If a source group name contains a literal `|`, escape it as `\|` inside the
filter value so it is not treated as a separator:

```Shell
INCLUDE_GROUP='ES\|*|\|ES\|*'
iptv-proxy --include-group 'ES\|*' --include-group '\|ES\|*'
```

### Playlist bootstrap, refresh and stable links

Metadata fetching has its own timeout; these settings do **not** change stream,
HLS or Xtream API deadlines. CLI flags override environment variables, which
override configuration-file/default values.

| CLI flag | Environment variable | Default |
| --- | --- | --- |
| `--playlist-fetch-timeout` | `PLAYLIST_FETCH_TIMEOUT` | `30s` |
| `--playlist-startup-timeout` | `PLAYLIST_STARTUP_TIMEOUT` | `0s` |
| `--playlist-refresh-interval` | `PLAYLIST_REFRESH_INTERVAL` | `0s` |
| `--playlist-state-dir` | `PLAYLIST_STATE_DIR` | empty (disabled) |
| `--playlist-stable-ids` | `PLAYLIST_STABLE_IDS` | `false` |
| `--m3u-source-ids` | `M3U_SOURCE_IDS` | unset |

Fetch timeout must be positive. Startup/refresh durations must be nonnegative.
With startup timeout `0s`, each source is attempted once. A positive timeout
bounds the whole bootstrap: failed sources retry after 5, 10, 20, then 30 seconds,
without downloading already-successful sources again. Each source's timeout
also covers reading its response body and cannot exceed the remaining startup
budget. SIGTERM/SIGINT cancel requests and retry waits.

Go owns catalogue generation; a container launcher should only check local
converter readiness, not pre-download the same playlists. Example deployment
settings for two aligned sources:

```Shell
PLAYLIST_FETCH_TIMEOUT=180s
PLAYLIST_STARTUP_TIMEOUT=600s
PLAYLIST_REFRESH_INTERVAL=6h
PLAYLIST_STATE_DIR=/var/lib/iptv-proxy
PLAYLIST_STABLE_IDS=true
M3U_SOURCE_IDS='primary|secondary'
```

Keep the existing `CUSTOM_ID` and relay settings. Source IDs must be nonempty,
unique and match `[A-Za-z0-9_-]{1,64}`; stable mode requires exactly one per
source. Source-ID changes are intentional channel-identity migrations.

Filtering happens during parsing while retaining discovered group names and
validating **every** record, including excluded records. Matching remains
case-sensitive, with the same literal, Unicode, wildcard and escaped-pipe
semantics. Empty/truncated/malformed source documents are rejected. A healthy
source may have no selected groups; the merged result must still contain at
least one selected track. This preserves existing multi-source filtering.

Each accepted catalogue is an immutable generation: playlist responses and
track lookup use its matching rendered bytes and metadata. A failed source,
invalid candidate or persistence failure retains the entire previous generation.
Refresh never replaces the live relay manager or disconnects existing viewers.
The optional timer and `SIGHUP` refresh are serialized and bursts are coalesced;
there is no public refresh endpoint. Through Supervisor, send
`supervisorctl signal HUP iptv-proxy` over its private control socket.

**Stable mode requires a one-time client playlist refresh.** Routes use
`/<CUSTOM_ID>/<proxy-user>/<proxy-password>/s<64 lowercase hex>/<filename>`.
Numeric positional links return **410 Gone** only in stable mode. Removed
channels return **404** for new requests, while existing viewers can finish.
Legacy mode keeps positional URLs. Set a fixed `CUSTOM_ID` if links must survive
process restarts; its legacy default is a newly generated prefix.
If refresh is explicitly enabled in legacy mode, reordered providers can still
change what positional links mean; use stable mode for durable channel links.

Recognized Xtream live/movie/series URLs with numeric provider IDs hash the
source ID, content kind and provider ID, not list position, name, hostname or
password. The source must represent at most one recognized account, including
excluded records. Unknown layouts hash the source ID and full URI without its
fragment; query tokens remain significant, so rotating those tokens can change
links. Conflicting upstream URLs for one stable key reject the candidate.
Automatic refresh rejects recognized account-identity changes (including
origin/credential changes) with `account_identity_changed`, so it cannot bypass
occupied-account protection. Planned provider/account changes require explicit
configuration and restart coordination. Stable mode serves the configured M3U
through the catalogue even for a single auto-detected Xtream source; optional
Xtream API endpoints and legacy auto mode remain available.

#### Private last-good metadata and status

The configured state directory is private (`0700`), and `catalogue.json` is
`0600`. It contains **credential-bearing upstream metadata**, not public rendered
playlist bytes. Keep it outside Git, public web roots and unprotected backups.
Use a dedicated local filesystem supporting same-directory atomic rename,
hard links and directory fsync; do not share one state directory between proxy
processes. State writes sync a staged file, retain a rollback link, atomically
rename and sync the directory before publishing in memory.

Startup validates schema version, source/filter/identity fingerprint, permissions
and metadata. Source URL/password/hostname or filter changes invalidate old state;
proxy credentials, advertised host/port and URL prefix do not: rendered output
is regenerated from current configuration. Valid state is served immediately,
with an asynchronous refresh. Missing state triggers bootstrap; rejected state
logs an explicit safe reason and attempts a fresh bounded bootstrap. State
larger than 128 MiB is not persisted or restored. Catastrophic filesystem failure that prevents
both directory sync and rollback requires operator recovery from a private backup.

`/status` adds `catalogue` with:

- `ready`, `generation`, selected `count`, `last_successful_refresh`;
- `last_refresh_error_kind` and `refreshing`;
- `sources`: source `id`, selected `count`, validated `total_count`,
  `last_attempt`, `last_success`, and `last_error_kind`.

Source timestamps describe fetch health; the catalogue timestamp describes
publication. Stale metadata or a refresh error does not make an otherwise usable
catalogue unready and is not permission to restart playback. Source URLs never
appear in status. M3U request logs whitelist method, response status, duration and
opaque route identity, including in Gin debug mode. Wrapped direct-stream HTTP
errors and direct connection identities do not expose upstream credentials.
Gin's built-in diagnostic writers are disabled once at startup because redirect
diagnostics run before middleware and include raw URLs. Redirect responses are
unchanged; safe application logging remains enabled.
Detailed status remains unauthenticated for local monitoring; restrict it at
ingress rather than trusting forwarded-client headers.

Validate rollout with synthetic providers first, then arrange client-link
migration and an approved playback observation. This change makes no throughput,
memory-reduction or uninterrupted-provider-reconnect claim.

### Shared live relay (enabled by default)

Multiple viewers of the **same live MPEG-TS source** share one upstream
connection. This covers M3U tracks with a `.ts` URL path and non-positive
`EXTINF` duration, plus Xtream `/live/user/password/id` and legacy
`/user/password/id` live endpoints (`.ts` or extensionless). Query tokens are
preserved. HLS playlists/segments, movies, series, timeshift, positive-duration
M3U tracks and other formats keep their existing direct behavior.

**There is no playback cache, replay, disk recording or deliberate delay.**
New viewers join the live edge. Each viewer has only a bounded transport queue
(256 chunks of at most 6,016 bytes, about 1.47 MiB) to absorb provider bursts
and short scheduling delays. A viewer that cannot keep up
is disconnected rather than silently losing bytes or blocking other viewers.
Native HTTP/1 downstream writes have a five-second deadline. A small TS
packet-boundary aligner discards an incomplete prefix; it does not retain
PAT/PMT, parse codecs or wait for keyframes. Players may therefore wait for the
provider's next stream tables/keyframe before showing a picture.

After the last viewer leaves, the upstream remains open for **30 seconds**.
Rejoining the same source during that grace period reuses it without replaying
bytes received while idle. Set the idle timeout to `0s` to stop immediately.
**Grace holds a provider connection slot:** switching to a *different* channel
on a one-slot account can still contend with the old channel. Sharing is not
cross-provider balancing or alternate-channel fallback. Providers can also take
additional time to release a slot after the proxy closes its connection.

EOF, network failures and read stalls reconnect to the **same source** with
exponential backoff, closing the previous upstream first. Read timeout means
inactivity, not maximum viewing time. Without playback buffering, short freezes
or player resynchronization during reconnect are unavoidable. Startup waits at
most 20 seconds for valid TS packets and then returns an HTTP error (including
a new viewer joining during a stalled reconnect); permanent
4xx responses (including 401/403/404, excluding transient 408/429) stop retries.
Cancelling one viewer does not cancel other viewers. Idle expiry and server
shutdown cancel upstream reads and reconnect waits.

| CLI flag | Environment variable | Default |
| --- | --- | --- |
| `--relay-enabled` | `RELAY_ENABLED` | `true` |
| `--relay-idle-timeout` | `RELAY_IDLE_TIMEOUT` | `30s` |
| `--relay-reconnect-initial` | `RELAY_RECONNECT_INITIAL` | `1s` |
| `--relay-reconnect-max` | `RELAY_RECONNECT_MAX` | `10s` |
| `--relay-read-timeout` | `RELAY_READ_TIMEOUT` | `15s` |

Durations require units (for example `250ms`, `15s`, `1m`). Invalid or negative
durations are rejected; reconnect/read durations must be positive and maximum
backoff must be at least the initial backoff. Use `RELAY_ENABLED=false` (or
`--relay-enabled=false`) to restore one direct upstream per viewer.

Sessions are keyed by the **exact upstream URL**, including query credentials,
and forwarded request headers. Authorization, cookies, Referer and Origin
differences **isolate sessions**, as do other custom forwarded headers. Only
User-Agent and Accept differences are ignored for sharing: the first viewer's
values are used for that session and its reconnects. Per-viewer forwarding
metadata (`Forwarded`, `X-Forwarded-For`, `X-Real-IP`) is not sent upstream.
This conservative isolation can create multiple sessions when players supply
different provider-sensitive headers.

An absent Range or exactly `Range: bytes=0-` can share; other byte ranges stay
direct. Shared requests omit range/conditional validators and request identity
encoding. Responses keep Content-Type but remove finite-response length/range
metadata, validators and Set-Cookie, and use `Cache-Control: no-store`.
The built-in server serves HTTP/1; when embedding a handler on HTTP/2, requests
remain direct so connection-wide deadlines cannot interrupt unrelated streams.

`/status` retains `active_connections` and `connections` as **viewer** counts
and adds aggregate `relay` counts (`sessions`, `viewers`, `upstreams`,
`pending_starts`, `pending_cleanup`, `reconnects`, `slow_disconnects`). Idle
sessions can have an upstream but no viewers. Sessions stay counted while
starting or cleaning up; a nonzero session count is not idle. Relay and direct
connection entries use opaque hashed identities, not credential-bearing URLs.

#### Protect a one-connection account: existing channel wins

Set `RELAY_EXISTING_CHANNEL_WINS=true` (or `--relay-existing-channel-wins=true`)
to protect a recognized Xtream account from competing live-channel requests.
This mode is **off by default** and requires the relay to be enabled.

If one viewer is watching A and another requests B on the same account, both
receive **A**, using its existing upstream. No request for B reaches the provider.
The first request reserves the account even during startup or reconnection, so
simultaneous channel selections cannot open competing upstreams. As long as any
viewer remains on A, further channel selections on that account also receive A.
This includes the original viewer selecting another channel while their old
connection is still open; the proxy does not infer device identity from IPs.

After all viewers disconnect, requesting the same channel reuses the idle
session. Requesting a different channel closes the idle upstream and waits for
it to finish before opening the new one; it need not wait out the idle grace.
The provider may still need time to release its slot. Disconnect all viewers
before intentionally changing the shared channel.

Accounts are identified by upstream origin (scheme, hostname and port) and
decoded username/password in `/live/user/pass/channel`, legacy
`/user/pass/channel`, and corresponding Xtream movie/series/timeshift paths.
Different accounts and origins remain independent. Provider hostname aliases
are not automatically grouped; custom/token-only URL layouts are not protected.
Use consistent provider URLs for channels belonging to one account.

Existing header/authentication isolation still applies: incompatible forwarded
headers or URL user-info credentials receive HTTP 409 rather than sharing
another authorization context or opening a competing connection. For recognized
accounts, direct HLS, VOD, seeking/range and other non-relay requests also receive
HTTP 409 in this mode, even while idle, so they cannot bypass account protection.
Playlist/API downloads themselves are unaffected.

The player may still label the selection **B** while displaying **A**. Substituted
responses carry `X-IPTV-Relay-Substituted: true`; `/status` marks their connection
with `substituted: true` and hashed `requested_url`, while `url` identifies the
actually served relay. Aggregate relay status includes `existing_channel_wins`
and `channel_substitutions`. These fields and substitution logs never include
provider credentials or raw stream URLs.

### M3u8 Example

The m3u8 feature is like m3u.
The playlist should be in the m3u format and should contain all m3u8 tracks.

Sample of the original m3u file containing m3u8 track:
```Shell
#EXTM3U
#EXTINF:-1 tvg-ID="examplechanel1.com" tvg-name="chanel1" tvg-logo="http://ch.xyz/logo1.png" group-title="USA HD",CHANEL1-HD
http://iptvexample.net:1234/12/test/1.m3u8
#EXTINF:-1 tvg-ID="examplechanel2.com" tvg-name="chanel2" tvg-logo="http://ch.xyz/logo2.png" group-title="USA HD",CHANEL2-HD
http://iptvexample.net:1234/13/test/2.m3u8
```

### Xtream code client API example

```Bash
% iptv-proxy --m3u-url http://example.com:1234/get.php?username=user&password=pass&type=m3u_plus&output=m3u8 \
             --port 8080 \
             --hostname proxyexample.com \
             ## put xtream flags if you want to add xtream proxy
             --xtream-user xtream_user \
             --xtream-password xtream_password \
             --xtream-base-url http://example.com:1234 \
             --user test \
             --password passwordtest
             
```

What Xtream proxy do

 - convert xtream `xtream-user ` and `xtream-password` into new `user` and `password`
 - convert `xtream-base-url` with `hostname` and `port`
 
Original xtream credentials
 
 ```
 user: xtream_user
 password: xtream_password
 base-url: http://example.com:1234
 ```
 
New xtream credentials

 ```
 user: test
 password: passwordtest
 base-url: http://proxyexample.com:8080
 ```
 
 All xtream live, streams, vod, series... are proxyfied! 
 
 
 You can get the m3u file with the original Xtream api request:
 ```
 http://proxyexample.com:8080/get.php?username=test&password=passwordtest&type=m3u_plus&output=ts
 ```


## Installation

Download lasted [release](https://github.com/pierre-emmanuelJ/iptv-proxy/releases)

Or

`% go install` in root repository

## With Docker

### Prerequisite

 - Add an m3u URL in `docker-compose.yml` or add local file in `iptv` folder
 - `HOSTNAME` and `PORT` to expose
 - Expose same container port as the `PORT` ENV variable 

```Yaml
 ports:
       # have to be the same as ENV variable PORT
      - 8080:8080
 environment:
      # if you are using one or more remote files
      # M3U_SOURCE: "http://example.com:1234/get.php?username=user&password=pass&type=m3u_plus&output=m3u8|http://example.net/backup.m3u"
      # Legacy single-source option:
      # M3U_URL: http://example.com:1234/get.php?username=user&password=pass&type=m3u_plus&output=m3u8
      M3U_SOURCE: /root/iptv/iptv.m3u
      # Port to expose the IPTVs endpoints
      PORT: 8080
      # Hostname or IP to expose the IPTVs endpoints (for machine not for docker)
      HOSTNAME: localhost
      GIN_MODE: release
      # Optional group-title/category filter
      # INCLUDE_GROUP: "Sports|News"
      ## Xtream-code proxy configuration
      ## (put these env variables if you want to add xtream proxy)
      XTREAM_USER: xtream_user
      XTREAM_PASSWORD: xtream_password
      XTREAM_BASE_URL: "http://example.com:1234"
      USER: test
      PASSWORD: testpassword
      # Optional debugging and response caching
      # DEBUG_LOGGING: true
      # CACHE_FOLDER: /root/iptv/cache/
      # ERROR_DETAIL_LEVEL: simple
```

### Start

```
% docker-compose up -d
```

## TLS - https with traefik

Put files and folders of `./traekik` folder in root repo:
```Shell
$ cp -r ./traekik/* .
```

```Shell
$ mkdir config \
        && mkdir -p Traefik/etc/traefik \
        && mkdir -p Traefik/log
```


`docker-compose` sample with traefik:
```Yaml
version: "3"
services:
  iptv-proxy:
    build:
      context: .
      dockerfile: Dockerfile
    volumes:
      # If your are using local m3u file instead of m3u remote file
      # put your m3u file in this folder
      - ./iptv:/root/iptv
    container_name: "iptv-proxy"
    restart: on-failure
    labels:
      - "traefik.enable=true"
      - "traefik.http.routers.iptv-proxy.rule=Host(`iptv.proxyexample.xyz`)"
      - "traefik.http.routers.iptv-proxy.entrypoints=websecure"
      - "traefik.http.routers.iptv-proxy.tls.certresolver=mydnschallenge"
      - "traefik.http.services.iptv-proxy.loadbalancer.server.port=8080"
    environment:
      # if you are using one or more remote files
      # M3U_SOURCE: "https://example.com/iptvfile.m3u|https://example.net/backup.m3u"
      M3U_SOURCE: /root/iptv/iptv.m3u
      # Iptv-Proxy listening port
      PORT: 8080
      # Port to expose for Xtream or m3u file tracks endpoint
      ADVERTISED_PORT: 443
      # Hostname or IP to expose the IPTVs endpoints (for machine not for docker)
      HOSTNAME: iptv.proxyexample.xyz
      GIN_MODE: release
      # Inportant to activate https protocol on proxy links
      HTTPS: 1
      ## Xtream-code proxy configuration
      XTREAM_USER: xtream_user
      XTREAM_PASSWORD: xtream_password
      XTREAM_BASE_URL: "http://example.tv:1234"
      #will be used for m3u and xtream auth proxy
      USER: test
      PASSWORD: testpassword
      # Optional group-title/category filter
      # INCLUDE_GROUP: "Sports|News"
      # Optional debugging and response caching
      # DEBUG_LOGGING: true
      # CACHE_FOLDER: /root/iptv/cache/
      # ERROR_DETAIL_LEVEL: simple

  traefik:
    restart: always
    image: traefik:v2.4
    read_only: true
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - ./Traefik/traefik.yaml:/traefik.yaml:ro
      - ./Traefik/etc/traefik:/etc/traefik/
      - ./Traefik/log:/var/log/traefik/
```

Replace `iptv.proxyexample.xyz` in `docker-compose.yml` with your desired domain.

```Shell
$ docker-compose up -d
```

## TODO

there is basic auth just for testing.
change with a real auth with database and user management
and auth with token...

**ENJOY!**

## Powered by

- [cobra](https://github.com/spf13/cobra)
- [go.xtream-codes](https://github.com/tellytv/go.xtream-codes)
- [gin](https://github.com/gin-gonic/gin)

Grab me a beer 🍻

[![paypal](https://www.paypalobjects.com/en_US/i/btn/btn_donate_LG.gif)](https://www.paypal.com/donate?hosted_button_id=WQAAMQWJPKHUN)
