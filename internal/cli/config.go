package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// config is what `commy login` saves: one panel address and the token for it.
type config struct {
	URL    string `json:"url"`
	Token  string `json:"token"`
	CAFile string `json:"ca_file,omitempty"`
}

// configPath is $COMMY_CONFIG, or commy/config.json in the user's config directory.
func configPath(getenv func(string) string) (string, error) {
	if p := getenv("COMMY_CONFIG"); p != "" {
		return p, nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot find a place for the config file (set COMMY_CONFIG): %w", err)
	}

	return filepath.Join(dir, "commy", "config.json"), nil
}

// loadConfig reads the saved config. A missing file is not an error (an empty config). A file that
// other users can read is refused: it holds a token.
func loadConfig(path string) (config, error) {
	var c config

	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}

	if err != nil {
		return c, err
	}

	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return c, fmt.Errorf("%s can be read by other users (mode %o) and holds a token; run: chmod 600 %s", path, info.Mode().Perm(), path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}

	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("%s is not a valid config file: %w", path, err)
	}

	return c, nil
}

// saveConfig writes the config so that only its owner can read it, replacing the old file in one step.
func saveConfig(path string, c config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}

	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()

		return err
	}

	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()

		return err
	}

	if err := tmp.Close(); err != nil {
		return err
	}

	return os.Rename(tmp.Name(), path)
}
