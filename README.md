# NodeBlock Agent — Ubuntu Setup & Run Guide

This guide shows how to install Go, build the NodeBlock agent, register the
node on the first run, and run it normally afterward.

The agent uses only the Go standard library, so there are no third-party Go
packages to install.

---

## 1. Connect to your Ubuntu server

SSH into the server where you want to run the NodeBlock agent:

```bash
ssh <your-user>@<your-server-ip>
```

Then confirm the server is Ubuntu:

```bash
cat /etc/os-release
```

---

## 2. Install Go

The agent requires Go 1.22 or newer.

### Ubuntu / Debian

Run:

```bash
sudo apt update
sudo apt install -y golang-go
```

Check the installed version:

```bash
go version
```

You should see Go 1.22 or newer, for example:

```text
go version go1.22.x linux/amd64
```

If your server has an older Go version, install a newer Go release before
building the agent.

---

## 3. Get the agent source code

If you already copied/extracted the `agent` directory to the server, enter it:

```bash
cd ~/agent
```

Check that the Go files are present:

```bash
ls -la
```

You should see at least:

```text
go.mod
main.go
README.md
```

If the ZIP file is in your home directory, you can extract it with:

```bash
cd ~
unzip agent.zip
cd agent
```

If `unzip` is not installed:

```bash
sudo apt update
sudo apt install -y unzip
```

---

## 4. Verify the Go project

From the directory containing `go.mod`:

```bash
go mod tidy
```

This project uses the Go standard library only, so `go mod tidy` should not
need to download third-party dependencies.

Verify the project:

```bash
go test ./...
```

It is also okay if there are no test files.

---

## 5. Build the NodeBlock agent

Build the release binary:

```bash
go build -trimpath -ldflags="-s -w" -o nodeblock-agent .
```

Check that it was created:

```bash
ls -lh nodeblock-agent
```

Check the binary:

```bash
file nodeblock-agent
```

You should get an output similar to:

```text
ELF 64-bit LSB executable, x86-64
```

Optional: create a SHA-256 checksum:

```bash
sha256sum nodeblock-agent > nodeblock-agent.sha256
cat nodeblock-agent.sha256
```

---

# 6. First run — register the node

The first run needs these three values:

1. `NODEBLOCK_API_URL` — your NodeBlock backend URL
2. `NODEBLOCK_NODE_ID` — the Node ID from your dashboard
3. `NODEBLOCK_REGISTRATION_TOKEN` — the one-time registration token

For example:

```bash
export NODEBLOCK_API_URL="https://app.nodeblock.network"
export NODEBLOCK_NODE_ID="YOUR_NODE_ID"
export NODEBLOCK_REGISTRATION_TOKEN="YOUR_REGISTRATION_TOKEN"
```

Replace the placeholders with the real values from your NodeBlock dashboard.

Then start the agent:

```bash
./nodeblock-agent
```

A successful registration should produce messages similar to:

```text
[nodeblock-agent] node identity ready (pubkey ...)
[nodeblock-agent] registered node YOUR_NODE_ID
[nodeblock-agent] running: 1 share every 12s (max 10 per 120s window), heartbeat every 30s
```

Keep the terminal open while testing.

---

# 7. IMPORTANT — where the identity key is stored

On the first successful start, the agent creates:

```text
~/.nodeblock/identity.key
```

The file is created with owner-only permissions:

```text
0600
```

Check it:

```bash
ls -la ~/.nodeblock/
```

You should see:

```text
identity.key
```

The private identity key stays on the server and is not sent to the backend.

**Do not delete this file.**

Deleting it creates a new identity and can permanently break the identity
associated with the registered node.

---

# 8. Stop the agent after testing

Press:

```text
Ctrl+C
```

The agent handles `SIGINT`/`SIGTERM` and exits cleanly.

---

# 9. Run the agent after registration

After the node has already been registered, the registration token is no
longer required.

Set only:

```bash
export NODEBLOCK_API_URL="https://app.nodeblock.network"
export NODEBLOCK_NODE_ID="YOUR_NODE_ID"
```

Then:

```bash
./nodeblock-agent
```

Do NOT provide the registration token again unless the backend specifically
requires a new registration.

---

# 10. Recommended: create an environment file

Instead of manually exporting variables every time, create a protected
environment file.

Create:

```bash
nano ~/.nodeblock.env
```

Put this inside:

```bash
NODEBLOCK_API_URL="https://app.nodeblock.network"
NODEBLOCK_NODE_ID="YOUR_NODE_ID"
NODEBLOCK_REGISTRATION_TOKEN="YOUR_REGISTRATION_TOKEN"
```

Save the file and protect it:

```bash
chmod 600 ~/.nodeblock.env
```

Load it:

```bash
source ~/.nodeblock.env
```

Verify that the variables exist without printing the secret token:

```bash
echo "$NODEBLOCK_API_URL"
echo "$NODEBLOCK_NODE_ID"
```

Then run:

```bash
./nodeblock-agent
```

After the first successful registration, you can remove the registration
token from the environment file:

```bash
nano ~/.nodeblock.env
```

Leaving:

```bash
NODEBLOCK_API_URL="https://app.nodeblock.network"
NODEBLOCK_NODE_ID="YOUR_NODE_ID"
```

---

# 11. Run the agent as a systemd service

For a server, systemd is recommended because the agent can automatically
restart after a reboot or unexpected process exit.

First find the absolute path of the binary:

```bash
pwd
```

For example, if the output is:

```text
/home/ubuntu/agent
```

then the binary is:

```text
/home/ubuntu/agent/nodeblock-agent
```

Create a systemd service:

```bash
sudo nano /etc/systemd/system/nodeblock-agent.service
```

Use this configuration and change the paths/user if necessary:

```ini
[Unit]
Description=NodeBlock Node Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=ubuntu
WorkingDirectory=/home/ubuntu/agent
EnvironmentFile=/home/ubuntu/.nodeblock.env
ExecStart=/home/ubuntu/agent/nodeblock-agent
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### Important

Replace:

```text
ubuntu
```

with the actual Linux username that owns the agent.

Also replace:

```text
/home/ubuntu/agent
```

and:

```text
/home/ubuntu/.nodeblock.env
```

with the actual paths on your server.

---

## 12. Protect the systemd environment file

Make sure the environment file is readable only by the agent user:

```bash
chmod 600 ~/.nodeblock.env
```

If systemd cannot read the file because of permissions, verify the path and
ownership.

---

## 13. Enable and start the service

Reload systemd:

```bash
sudo systemctl daemon-reload
```

Enable the service at boot:

```bash
sudo systemctl enable nodeblock-agent
```

Start it:

```bash
sudo systemctl start nodeblock-agent
```

Check status:

```bash
sudo systemctl status nodeblock-agent
```

You want to see:

```text
Active: active (running)
```

---

# 14. View live agent logs

Use:

```bash
sudo journalctl -u nodeblock-agent -f
```

Press:

```text
Ctrl+C
```

to stop watching the logs.

View the last 100 lines:

```bash
sudo journalctl -u nodeblock-agent -n 100 --no-pager
```

---

# 15. Restart the agent

After changing configuration or replacing the binary:

```bash
sudo systemctl restart nodeblock-agent
```

Then check:

```bash
sudo systemctl status nodeblock-agent
```

---

# 16. Stop the agent

```bash
sudo systemctl stop nodeblock-agent
```

Disable automatic startup:

```bash
sudo systemctl disable nodeblock-agent
```

---

# 17. Verify that the agent is running

Check the process:

```bash
ps aux | grep nodeblock-agent
```

Check the service:

```bash
sudo systemctl is-active nodeblock-agent
```

Expected:

```text
active
```

---

# 18. What the agent does

Once running:

### Every 12 seconds

The agent signs a request and submits it to:

```text
/api/agent/<nodeId>/share
```

The server enforces the maximum of 10 shares per 120-second window.

### Every 30 seconds

The agent sends a signed heartbeat to:

```text
/api/agent/<nodeId>/heartbeat
```

The agent uses an Ed25519 private key stored locally in:

```text
~/.nodeblock/identity.key
```

The private key is not transmitted to the backend.

---

# 19. Troubleshooting

## `go: command not found`

Install Go:

```bash
sudo apt update
sudo apt install -y golang-go
```

Then:

```bash
go version
```

---

## `NODEBLOCK_API_URL and NODEBLOCK_NODE_ID ... are required`

Set the variables:

```bash
export NODEBLOCK_API_URL="https://app.nodeblock.network"
export NODEBLOCK_NODE_ID="YOUR_NODE_ID"
```

Then run:

```bash
./nodeblock-agent
```

---

## Registration fails

Make sure all three first-run variables are correct:

```bash
echo "$NODEBLOCK_API_URL"
echo "$NODEBLOCK_NODE_ID"
echo "$NODEBLOCK_REGISTRATION_TOKEN"
```

Check that:

- The API URL is correct.
- The Node ID belongs to the node you are registering.
- The registration token is the current one-time token.
- The server can reach the NodeBlock API over HTTPS.

Test basic connectivity:

```bash
curl -I https://app.nodeblock.network
```

---

## `permission denied` when running the binary

Make the binary executable:

```bash
chmod +x nodeblock-agent
```

Then:

```bash
./nodeblock-agent
```

---

## Identity error / corrupt identity

Check:

```bash
ls -l ~/.nodeblock/identity.key
```

Do not delete the identity file casually. It represents the local identity
of the registered node.

If the identity file is genuinely corrupt, stop the agent and contact the
NodeBlock backend/operator before creating a replacement identity.

---

## Check systemd logs

```bash
sudo journalctl -u nodeblock-agent -n 100 --no-pager
```

For live logs:

```bash
sudo journalctl -u nodeblock-agent -f
```

---

# 20. Complete quick-start

If the agent source is already on the server, these are the main commands:

```bash
sudo apt update
sudo apt install -y golang-go

cd ~/agent

go version
go mod tidy
go test ./...

go build -trimpath -ldflags="-s -w" -o nodeblock-agent .

export NODEBLOCK_API_URL="https://app.nodeblock.network"
export NODEBLOCK_NODE_ID="YOUR_NODE_ID"
export NODEBLOCK_REGISTRATION_TOKEN="YOUR_REGISTRATION_TOKEN"

./nodeblock-agent
```

After the first successful registration, stop it with `Ctrl+C` and run it
again without the registration token:

```bash
export NODEBLOCK_API_URL="https://app.nodeblock.network"
export NODEBLOCK_NODE_ID="YOUR_NODE_ID"

./nodeblock-agent
```

For a production server, use the systemd service described above.

---

# Security notes

- Never commit `NODEBLOCK_REGISTRATION_TOKEN` to Git.
- Never commit `~/.nodeblock/identity.key` to Git.
- Keep the environment file at mode `600`.
- Keep the identity key at mode `600`.
- Use HTTPS for `NODEBLOCK_API_URL`.
- Do not delete `~/.nodeblock/identity.key` unless you intentionally need to
  replace the node's identity.
