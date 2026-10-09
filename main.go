// NodeBlock node agent.
//
// Runs on the operator's machine, proves its identity to the backend with a
// locally-generated ed25519 keypair (never transmitted), and submits signed
// "share" pings on a fixed cadence. The 10-per-120s cap is enforced by the
// SERVER, not here — this agent's own pacing is just good behavior, not the
// security boundary. See README.md for why that split matters.
//
// Registration happens exactly once, on the very first run (when there's no
// local identity key yet). Every later run — including after you stop and
// start the agent — reuses that same key and node ID, so you never re-register
// and never need the one-time registration token again. The node ID and API
// URL from the first run are remembered in a small node.json next to the key,
// so a plain restart needs no flags or environment variables at all.
//
// Build a stripped release binary with:
//   go build -trimpath -ldflags="-s -w" -o nodeblock-agent .
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const (
	windowSeconds      = 120
	maxSharesPerWindow = 10
	shareInterval      = windowSeconds / maxSharesPerWindow // 12s: spread submissions evenly across the window
	heartbeatInterval  = 30 * time.Second
	httpTimeout        = 10 * time.Second
	keyFilePermissions = 0600
	defaultKeyFileName = "identity.key" // raw 64-byte ed25519 private key, local-only, never sent over the wire; stored next to the binary by default so one machine can run several agents, one per folder
	stateFileName      = "node.json"    // remembers nodeId + api URL so restarts need no flags
)

// errAlreadyRegisteredDifferentKey is returned when the backend reports this
// node is already bound to a DIFFERENT public key than the one we hold — i.e.
// a genuine conflict (lost key), not a normal restart.
var errAlreadyRegisteredDifferentKey = fmt.Errorf("node already registered with a different identity key")

type Config struct {
	APIBaseURL        string
	NodeID            string
	RegistrationToken string // only needed on the very first run
	KeyPath           string
}

type identity struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// persistedState is written next to the identity key after the first run so a
// later restart can recover the node ID and API URL without any flags.
type persistedState struct {
	NodeID     string `json:"nodeId"`
	APIBaseURL string `json:"apiBaseUrl"`
}

func main() {
	cfg := parseFlags()

	// Fill any missing nodeId / API URL from the state saved on the first run,
	// so a bare `./nodeblock-agent` restart just works.
	st := loadState(cfg.KeyPath)
	if cfg.NodeID == "" {
		cfg.NodeID = st.NodeID
	}
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = st.APIBaseURL
	}
	if cfg.APIBaseURL == "" || cfg.NodeID == "" {
		log.Fatalf("NODEBLOCK_API_URL and NODEBLOCK_NODE_ID (or -api/-node-id) are required on the first run")
	}

	id, created, err := loadOrCreateIdentity(cfg.KeyPath)
	if err != nil {
		log.Fatalf("identity error: %v", err)
	}
	log.Printf("[nodeblock-agent] node identity ready (pubkey %s...)", hex.EncodeToString(id.pub)[:12])

	if created {
		// First run on this machine: no local key existed, so we just made one
		// and must bind it to the node with the one-time registration token.
		if cfg.RegistrationToken == "" {
			log.Fatalf("first run detected (new identity key created at %s) but no registration token was given.\n"+
				"Pass -registration-token (or set NODEBLOCK_REGISTRATION_TOKEN) from the dashboard — first run only.", cfg.KeyPath)
		}
		if err := register(cfg, id); err != nil {
			if err == errAlreadyRegisteredDifferentKey {
				log.Fatalf("this node is already registered with a different identity key, but the key at %s is new.\n"+
					"If you lost the original key, reset this node's key from the dashboard, then run again.", cfg.KeyPath)
			}
			log.Fatalf("registration failed: %v", err)
		}
		log.Printf("[nodeblock-agent] registered node %s", cfg.NodeID)
	} else {
		// Restart: we already hold the identity this node was registered with,
		// so there is nothing to register. Go straight to work. The one-time
		// token (if someone still passes it) is not needed and is ignored.
		log.Printf("[nodeblock-agent] existing identity found at %s — skipping registration (already bound)", cfg.KeyPath)
		if cfg.RegistrationToken != "" {
			log.Printf("[nodeblock-agent] note: a registration token was supplied but isn't needed on restart; ignoring it")
		}
	}

	// Remember nodeId + API URL so the next restart needs no flags at all.
	if err := saveState(cfg); err != nil {
		log.Printf("[warn] could not save node state (%v); restarts will still need -node-id/-api", err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	shareTicker := time.NewTicker(shareInterval * time.Second)
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer shareTicker.Stop()
	defer heartbeatTicker.Stop()

	log.Printf("[nodeblock-agent] running: 1 share every %ds (max %d per %ds window), heartbeat every %s",
		shareInterval, maxSharesPerWindow, windowSeconds, heartbeatInterval)

	for {
		select {
		case <-shareTicker.C:
			if err := submitShare(cfg, id); err != nil {
				log.Printf("[warn] share submission failed: %v", err)
			}
		case <-heartbeatTicker.C:
			if err := sendHeartbeat(cfg, id); err != nil {
				log.Printf("[warn] heartbeat failed: %v", err)
			}
		case <-stop:
			log.Println("[nodeblock-agent] shutting down")
			return
		}
	}
}

func parseFlags() Config {
	apiURL := flag.String("api", os.Getenv("NODEBLOCK_API_URL"), "NodeBlock backend base URL, e.g. https://api.nodeblock.example")
	nodeID := flag.String("node-id", os.Getenv("NODEBLOCK_NODE_ID"), "Node ID assigned in the dashboard (first run only; remembered afterward)")
	regToken := flag.String("registration-token", os.Getenv("NODEBLOCK_REGISTRATION_TOKEN"), "One-time registration token from the dashboard (first run only)")
	keyPath := flag.String("key-path", os.Getenv("NODEBLOCK_KEY_PATH"), "Path to store the local identity key")
	flag.Parse()

	kp := *keyPath
	if kp == "" {
		// Default the identity key to the agent binary's OWN folder, so you can
		// run several agents on one machine — one per folder, each with its own
		// copy of the binary — and they each keep a separate identity.key and
		// node.json instead of fighting over one shared file. This is resolved
		// from the executable's location (not the working directory), so it
		// still lands in the folder when launched from a service or a script.
		// Override anytime with -key-path or NODEBLOCK_KEY_PATH.
		baseDir := "."
		if exePath, err := os.Executable(); err == nil {
			baseDir = filepath.Dir(exePath)
		}
		kp = filepath.Join(baseDir, defaultKeyFileName)
	}
	// Note: we intentionally do NOT exit here if node-id/api are missing —
	// they may be recovered from saved state in main().
	return Config{APIBaseURL: *apiURL, NodeID: *nodeID, RegistrationToken: *regToken, KeyPath: kp}
}

// loadOrCreateIdentity generates a fresh ed25519 keypair the first time the
// agent runs and persists only the private key, locally, with owner-only
// permissions. Every later run re-derives the public key from it — the
// backend's copy of the public key never changes for this node's lifetime.
//
// Returns created=true only when a brand-new key was generated this run; that
// is the one and only situation in which registration is needed.
func loadOrCreateIdentity(keyPath string) (*identity, bool, error) {
	if data, err := os.ReadFile(keyPath); err == nil {
		if len(data) != ed25519.PrivateKeySize {
			return nil, false, fmt.Errorf("identity file %s is corrupt (expected %d bytes, got %d)", keyPath, ed25519.PrivateKeySize, len(data))
		}
		priv := ed25519.PrivateKey(data)
		return &identity{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, false, nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(keyPath, priv, keyFilePermissions); err != nil {
		return nil, false, err
	}
	return &identity{priv: priv, pub: pub}, true, nil
}

func statePath(keyPath string) string {
	return filepath.Join(filepath.Dir(keyPath), stateFileName)
}

func loadState(keyPath string) persistedState {
	var st persistedState
	if data, err := os.ReadFile(statePath(keyPath)); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func saveState(cfg Config) error {
	st := persistedState{NodeID: cfg.NodeID, APIBaseURL: cfg.APIBaseURL}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.KeyPath), 0700); err != nil {
		return err
	}
	return os.WriteFile(statePath(cfg.KeyPath), data, keyFilePermissions)
}

// register binds this agent's public key to its node. The backend treats a
// re-registration with the SAME key as success (idempotent), so this is safe
// to call; it only fails hard when a different key already owns the node.
func register(cfg Config, id *identity) error {
	body, _ := json.Marshal(map[string]string{
		"nodeId":            cfg.NodeID,
		"registrationToken": cfg.RegistrationToken,
		"publicKey":         base64.StdEncoding.EncodeToString(id.pub),
	})
	req, err := http.NewRequest("POST", cfg.APIBaseURL+"/api/agent/register", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)

	// 2xx: newly registered (201) or idempotent re-confirm of the same key (200).
	if resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == 409 {
		return errAlreadyRegisteredDifferentKey
	}
	return fmt.Errorf("/api/agent/register -> %d: %s", resp.StatusCode, string(b))
}

func sendHeartbeat(cfg Config, id *identity) error {
	path := fmt.Sprintf("/api/agent/%s/heartbeat", cfg.NodeID)
	return signedPost(cfg, id, path)
}

func submitShare(cfg Config, id *identity) error {
	path := fmt.Sprintf("/api/agent/%s/share", cfg.NodeID)
	return signedPost(cfg, id, path)
}

// signedPost signs `${nodeId}.${path}.${timestamp}.${nonce}` with the local
// private key and sends it as headers. The backend verifies this against
// the public key it stored at registration time — a modified binary still
// can't forge a valid signature without this exact private key, and can't
// replay an old one because of the nonce + timestamp window.
func signedPost(cfg Config, id *identity, path string) error {
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := randomNonce()
	message := fmt.Sprintf("%s.%s.%s.%s", cfg.NodeID, path, timestamp, nonce)
	signature := ed25519.Sign(id.priv, []byte(message))

	req, err := http.NewRequest("POST", cfg.APIBaseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-node-id", cfg.NodeID)
	req.Header.Set("x-timestamp", timestamp)
	req.Header.Set("x-nonce", nonce)
	req.Header.Set("x-signature", base64.StdEncoding.EncodeToString(signature))
	return doAndCheck(req)
}

func doAndCheck(req *http.Request) error {
	client := &http.Client{Timeout: httpTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s -> %d: %s", req.URL.Path, resp.StatusCode, string(b))
	}
	return nil
}

func randomNonce() string {
	buf := make([]byte, 12)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
