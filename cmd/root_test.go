package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

// useTestConfig gives the test a clean viper, an empty working directory and
// an empty home directory, so it can't find a real configuration. It returns
// the paths of the two directories.
func useTestConfig(t *testing.T) (dir, home string) {
	t.Helper()
	resetViper := func() {
		viper.Reset()
		setupViper()
	}
	resetViper()
	t.Cleanup(resetViper)

	dir, home = t.TempDir(), t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", home)
	t.Setenv("IRIS_CONFIG", "")
	t.Setenv("IRIS_ADDR", "")
	return dir, home
}

// writeConfig writes a configuration file setting addr, and returns its path.
func writeConfig(t *testing.T, dir, addr string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("addr: "+addr+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigFromEnvBeatsWorkingDirectory(t *testing.T) {
	dir, _ := useTestConfig(t)
	writeConfig(t, dir, "decoy:1")
	t.Setenv("IRIS_CONFIG", writeConfig(t, t.TempDir(), "chosen:1"))

	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	if got := viper.GetString("addr"); got != "chosen:1" {
		t.Errorf("addr = %q", got)
	}
}

func TestConfigFromEnvMissingFile(t *testing.T) {
	dir, _ := useTestConfig(t)
	writeConfig(t, dir, "decoy:1")
	t.Setenv("IRIS_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))

	if err := loadConfig(); !os.IsNotExist(err) {
		t.Errorf("got %v", err)
	}
	if got := viper.GetString("addr"); got == "decoy:1" {
		t.Errorf("fell back to the search path: addr = %q", got)
	}
}

func TestConfigSearchPath(t *testing.T) {
	t.Run("working directory", func(t *testing.T) {
		dir, home := useTestConfig(t)
		writeConfig(t, dir, "cwd:1")
		writeConfig(t, filepath.Join(home, ".config", "iris"), "home:1")

		if err := loadConfig(); err != nil {
			t.Fatal(err)
		}
		if got := viper.GetString("addr"); got != "cwd:1" {
			t.Errorf("addr = %q", got)
		}
	})

	t.Run("home directory", func(t *testing.T) {
		_, home := useTestConfig(t)
		writeConfig(t, filepath.Join(home, ".config", "iris"), "home:1")

		if err := loadConfig(); err != nil {
			t.Fatal(err)
		}
		if got := viper.GetString("addr"); got != "home:1" {
			t.Errorf("addr = %q", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		useTestConfig(t)

		if err := loadConfig(); err == nil {
			t.Error("no error")
		}
	})
}

func TestEnvOverridesConfigFile(t *testing.T) {
	dir, _ := useTestConfig(t)
	writeConfig(t, dir, "file:1")
	t.Setenv("IRIS_ADDR", "env:1")

	if err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	if got := viper.GetString("addr"); got != "env:1" {
		t.Errorf("addr = %q", got)
	}
}
