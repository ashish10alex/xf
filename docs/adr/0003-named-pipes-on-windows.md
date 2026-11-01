---
status: accepted
---

# Reach xf serve over a named pipe on Windows, a Unix socket elsewhere

xf's clients (the TUI, `xf lsp`, and later a VS Code extension) share one `xf serve` per project, backend and variant over a Unix domain socket guarded by a `flock` lock. On Windows, `xf serve` instead listens on a named pipe, `\\.\pipe\xf-<hash>-<backend>-<variant>-v<N>`, through Microsoft's go-winio. Pipe names are shared by every user on the machine, so on Windows the hash covers the user's SID as well as the project's real path. macOS and Linux keep the Unix socket.

Go can listen on AF_UNIX on Windows 10 1803 and later, so keeping the socket would have needed no new dependency. It loses on two counts. libuv, under both Node and neovim, treats an IPC path on Windows as a named pipe, so a VS Code extension could never reach a Unix socket directly. A plan to move the Dataform Tools extension onto xf relies on that connection. And `flock` doesn't exist on Windows.

A named pipe answers both. Node, VS Code and neovim connect to it natively. The pipe's first-instance flag serves as the single-server lock, so there is no lock file. A pipe leaves no stale file behind: it vanishes once its server and every client have closed it. Until the clients notice a dead server, its name lingers, so `Listen` retries for a couple of seconds before deciding a server is running. go-winio only takes connections while `Accept` is pending, so a name that exists but doesn't answer within that time counts as running too. The pipe is created with a security descriptor that admits only the current user, because go-winio's default lets Everyone read it. Clients check the pipe's owner and access list before trusting it, so another user can't create the name first and pose as the server.

## Considered options

- **AF_UNIX on Windows, plus an `xf bridge` stdio subcommand for Node clients.** No new dependency and one transport everywhere, but it costs an extra process per client, leaves stale socket files to clean up, and still needs a replacement for `flock`.
- **Loopback TCP with a token in a file.** Works on every OS, but means managing tokens, and corporate firewall and EDR prompts on Windows are a risk.

## Consequences

- `internal/remote` gains one dial and listen implementation per OS. The rest of the wire protocol (Content-Length-framed JSON-RPC) is unchanged, so the protocol version doesn't change because of the transport.
- On Windows there is no `.lock` or `.sock` file. The log file still sits in the cache directory beside where the socket would be.
