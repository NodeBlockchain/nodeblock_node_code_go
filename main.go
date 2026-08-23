// NodeBlock node agent.
//
// Runs on the operator's machine, proves its identity to the backend with a
// locally-generated ed25519 keypair (never transmitted), and submits signed
// "share" pings on a fixed cadence. The 10-per-120s cap is enforced by the
// SERVER, not here — this agent's own pacing is just good behavior, not the
// security boundary. See README.md for why that split matters.
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
	windowSeconds       = 120
	maxSharesPerWindow  = 10
	shareInterval       = windowSeconds / maxSharesPerWindow // 12s: spread submissions evenly across the window
	heartbeatInterval   = 30 * time.Second
	httpTimeout         = 10 * time.Second
	keyFilePermissions  = 0600
	defaultKeyFileName  = "identity.key" // raw 64-byte ed25519 private key, local-only, never sent over the wire
)

type Config struct {
	APIBaseURL         string
	NodeID             string
	RegistrationToken  string // only needed on first run
	KeyPath            string
}

type identity struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

func main() {
	cfg := parseFlags()

	id, err := loadOrCreateIdentity(cfg.KeyPath)
	if err != nil {
		log.Fatalf("identity error: %v", err)
	}
	log.Printf("[nodeblock-agent] node identity ready (pubkey %s...)", hex.EncodeToString(id.pub)[:12])

	if cfg.RegistrationToken != "" {
		if err := register(cfg, id); err != nil {
			log.Fatalf("registration failed: %v", err)
		}
		log.Printf("[nodeblock-agent] registered node %s", cfg.NodeID)
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
	nodeID := flag.String("node-id", os.Getenv("NODEBLOCK_NODE_ID"), "Node ID assigned in the dashboard")
	regToken := flag.String("registration-token", os.Getenv("NODEBLOCK_REGISTRATION_TOKEN"), "One-time registration token from the dashboard (first run only)")
	keyPath := flag.String("key-path", os.Getenv("NODEBLOCK_KEY_PATH"), "Path to store the local identity key")
	flag.Parse()

	if *apiURL == "" || *nodeID == "" {
		fmt.Fprintln(os.Stderr, "NODEBLOCK_API_URL and NODEBLOCK_NODE_ID (or -api/-node-id) are required")
		os.Exit(1)
	}
	if *keyPath == "" {
		dir, _ := os.UserHomeDir()
		*keyPath = filepath.Join(dir, ".nodeblock", defaultKeyFileName)
	}
	return Config{APIBaseURL: *apiURL, NodeID: *nodeID, RegistrationToken: *regToken, KeyPath: *keyPath}
}

// loadOrCreateIdentity generates a fresh ed25519 keypair the first time the
// agent runs and persists only the private key, locally, with owner-only
// permissions. Every later run re-derives the public key from it — the
// backend's copy of the public key never changes for this node's lifetime.
func loadOrCreateIdentity(keyPath string) (*identity, error) {
	if data, err := os.ReadFile(keyPath); err == nil {
		if len(data) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("identity file %s is corrupt", keyPath)
		}
		priv := ed25519.PrivateKey(data)
		return &identity{priv: priv, pub: priv.Public().(ed25519.PublicKey)}, nil
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, priv, keyFilePermissions); err != nil {
		return nil, err
	}
	return &identity{priv: priv, pub: pub}, nil
}

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
	return doAndCheck(req)
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
