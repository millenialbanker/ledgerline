package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// config is what the signer keeps between runs. The agent key is stored
// protected (see protect); the PIN is never stored.
type config struct {
	Server     string `json:"server"`
	Key        string `json:"agent_key_protected"`
	Thumbprint string `json:"certificate_sha1,omitempty"` // the token certificate chosen (SHA-1, hex)
	PFX        string `json:"pfx,omitempty"`              // a certificate file instead of a token
}

// defaultConfigPath is %APPDATA%\Ledgerline Signer\config.json on Windows
// (the user's configuration folder elsewhere).
func defaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "Ledgerline Signer", "config.json")
}

func loadConfig(path string) (config, error) {
	var c config
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, errors.New("not set up yet")
		}
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

func saveConfig(path string, c config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(path, b, 0o600)
}
