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
`reconnects`, `slow_disconnects`). Idle sessions can have an upstream but no
viewers. Relay connection entries and reconnect logs use a hashed session ID,
not credential-bearing provider URLs. Existing direct-stream status behavior
is unchanged.

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
