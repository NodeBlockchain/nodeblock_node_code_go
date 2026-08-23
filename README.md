# NodeBlock node agent

Go, standard library only — no third-party modules to compromise or swap
out, and no network dependency at build time beyond the Go toolchain
itself.

## Build

```bash
cd agent
go build -trimpath -ldflags="-s -w" -o nodeblock-agent .
```

`-trimpath` strips local filesystem paths from the binary; `-ldflags="-s -w"`
strips the symbol table and DWARF debug info, so the binary doesn't hand an
attacker a readable map of your logic. This raises the cost of tampering —
it does not make it impossible. **The actual security boundary is the
backend's signature check and rate limit, not this binary's opacity.** See
the root `README.md` for why that split is the honest way to design this.

Publish a SHA-256 checksum of each release binary alongside the download so
operators can verify they're running what you built:

```bash
sha256sum nodeblock-agent > nodeblock-agent.sha256
```

## Run

First run (registers the node):

```bash
export NODEBLOCK_API_URL="https://api.nodeblock.example"
export NODEBLOCK_NODE_ID="<nodeId from the dashboard>"
export NODEBLOCK_REGISTRATION_TOKEN="<one-time token from the dashboard>"
./nodeblock-agent
```

Every run after that (registration token isn't needed once registered —
the agent already has its local identity key):

```bash
export NODEBLOCK_API_URL="https://api.nodeblock.example"
export NODEBLOCK_NODE_ID="<nodeId>"
./nodeblock-agent
```

The identity private key is written once to `~/.nodeblock/identity.key`
(mode `0600`, owner read/write only) and reused on every subsequent run —
delete it only if you intend to lose this node's identity permanently.

## What it actually does, every cycle

- Every 12 seconds (120s ÷ 10): signs `nodeId.path.timestamp.nonce` with the
  local ed25519 private key, POSTs it to `/api/agent/:nodeId/share`.
- Every 30 seconds: same signing scheme, POSTs to
  `/api/agent/:nodeId/heartbeat`.
- The agent does not try to out-pace or exceed the cap — there's no benefit
  to it, since the server silently drops anything past 10 in a window and
  logs it as a rejected/flagged submission on that node's record.

## Running it as a service

Use systemd, a process supervisor, or a container — whatever your fleet
already uses. The agent has no daemonizing logic itself; it runs in the
foreground and exits cleanly on `SIGINT`/`SIGTERM`.
