---
status: accepted
---

# xf web sends its events over a WebSocket

The page's event stream (`/api/events`) is a WebSocket, not server-sent events.
Each message is `{"event": name, "data": …}`, the names and data as the events were before; the first is `hello`.
The server pings an idle page every 20 seconds, and a page that says nothing, not even the pong, for a minute counts as gone.
The page reconnects by itself after a second, then less often up to every 10 seconds, and asks again for what it shows, as it did when its event source reconnected.
It does not reconnect after the `closed` or `quit` event, or when it leaves for another project.

The WebSocket is written in `internal/web/websocket.go` (RFC 6455, about 200 lines) rather than taken from a library: the server only sends text frames, answers pings and closes, and pings; what a page sends is read and dropped.
It goes through the same guard as every request: a loopback or allowed host, an `Origin` naming this server, a same-origin `Sec-Fetch-Site`, and the token where ADR 0004 needs it.

## Why

A browser lets pages have six HTTP/1.1 connections to one host and port, shared by all its tabs and windows.
Each tab's event source held one for as long as it was open, so with four tabs only two connections were left for every tab's requests, and a slow one (a file's dry-run diagnostics take up to 20 seconds) held up the rest: the source pane loaded late and `gd`, three requests one after another, felt slow.
With six tabs, no request finished at all.
The server answered each request in 2 to 100 ms; the time was spent waiting for a connection.
WebSockets are counted apart from those six.

## Considered options

- **Close the stream in hidden tabs.**
  No new code on the server, but two windows side by side are both visible, a hidden tab misses events until it resyncs, and auto-exit counts streams, so every tab hidden would start the countdown.
- **One stream per browser, shared by its tabs** (a SharedWorker, or a tab elected over BroadcastChannel).
  One connection whatever the number of tabs, but each tab's own id (its unsaved files, auto-exit) would need passing through the shared stream, and a leader tab closing hands the stream to another.
- **HTTP/2.** Many requests on one connection, but browsers speak it only over TLS, and xf web serves plain HTTP on loopback.
- **A WebSocket library** (`github.com/coder/websocket`). Complete, but the stream uses a small part of the protocol, and xf has kept its dependencies few.

## Consequences

- This reverses ADR 0004's choice of server-sent events.
  Its reason, that they reconnect by themselves and need no dependency, is kept by a short reconnect in `state.js` and no new dependency.
- A proxy in front of xf web (`--allow-host` through Tailscale and the like) must pass WebSocket upgrades. Tailscale's does.
- After the upgrade the server no longer tracks the connection, so stopping xf web ends the streams by closing them rather than by `http.Server.Shutdown` waiting for them.
