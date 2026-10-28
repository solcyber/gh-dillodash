package config

import (
	"os"
	"path/filepath"
)

// EnvVar names the environment override for the config path.
const EnvVar = "DILLODASH_CONFIG"

// FindPath picks the config file, in order: cliPath (which must exist),
// $DILLODASH_CONFIG (which must exist), $XDG_CONFIG_HOME/dillodash/
// config.toml, ~/.config/dillodash/config.toml. path is "" when none of
// the implicit locations has a file. legacyINI is ~/.config/pr-dash/
// config.ini when it exists and no TOML was found, so the caller can offer
// MigrateINI. env supplies DILLODASH_CONFIG, XDG_CONFIG_HOME and HOME.
func FindPath(cliPath string, env func(string) string) (path string, legacyINI string, err error) {
	if cliPath != "" {
		if !isFile(cliPath) {
			return "", "", &Error{Path: cliPath, Why: "not found"}
		}
		return cliPath, "", nil
	}
	if p := env(EnvVar); p != "" {
		if !isFile(p) {
			return "", "", &Error{Path: p, Why: "not found ($" + EnvVar + ")"}
		}
		return p, "", nil
	}
	home := env("HOME")
	configHome := env("XDG_CONFIG_HOME")
	if configHome == "" && home != "" {
		configHome = filepath.Join(home, ".config")
	}
	if configHome != "" {
		if p := filepath.Join(configHome, "dillodash", "config.toml"); isFile(p) {
			return p, "", nil
		}
		if home != "" {
			if p := filepath.Join(home, ".config", "dillodash", "config.toml"); isFile(p) {
				return p, "", nil
			}
		}
	}
	if home != "" {
		if p := filepath.Join(home, ".config", "pr-dash", "config.ini"); isFile(p) {
			legacyINI = p
		}
	}
	return "", legacyINI, nil
}

// DefaultPath is where a fresh config goes: $XDG_CONFIG_HOME/dillodash/
// config.toml, or ~/.config/dillodash/config.toml.
func DefaultPath(env func(string) string) string {
	if x := env("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "dillodash", "config.toml")
	}
	return filepath.Join(env("HOME"), ".config", "dillodash", "config.toml")
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
