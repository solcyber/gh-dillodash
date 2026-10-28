package config

import (
	"os"
	"path/filepath"
	"runtime"
)

// EnvVar names the environment override for the config path.
const EnvVar = "DILLODASH_CONFIG"

// homeDir is the home directory: $HOME, else %USERPROFILE% (Windows
// shells rarely set HOME), else what the OS says.
func homeDir(env func(string) string) string {
	if h := env("HOME"); h != "" {
		return h
	}
	if h := env("USERPROFILE"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// configHome is where per-user config lives: $XDG_CONFIG_HOME, else
// %APPDATA% on Windows, else ~/.config. "" when no home is known.
func configHome(env func(string) string) string {
	if x := env("XDG_CONFIG_HOME"); x != "" {
		return x
	}
	if runtime.GOOS == "windows" {
		if a := env("APPDATA"); a != "" {
			return a
		}
	}
	if h := homeDir(env); h != "" {
		return filepath.Join(h, ".config")
	}
	return ""
}

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
	home := homeDir(env)
	if ch := configHome(env); ch != "" {
		if p := filepath.Join(ch, "dillodash", "config.toml"); isFile(p) {
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
// config.toml, %APPDATA%\dillodash\config.toml on Windows, else
// ~/.config/dillodash/config.toml.
func DefaultPath(env func(string) string) string {
	return filepath.Join(configHome(env), "dillodash", "config.toml")
}

// InstallPath is where config init, migrate and pull write when no
// --config is given: $DILLODASH_CONFIG when set (so the dashboard reads
// what was just written), else DefaultPath.
func InstallPath(env func(string) string) string {
	if p := env(EnvVar); p != "" {
		return p
	}
	return DefaultPath(env)
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
