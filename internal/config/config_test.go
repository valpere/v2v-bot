package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func inDir(t *testing.T, dotenv string) {
	t.Helper()
	for _, k := range []string{"TELEGRAM_BOT_TOKEN", "ENGINE_URL", "ENGINE_KEY", "ENGINE_TIMEOUT"} {
		t.Setenv(k, "")
	}
	dir := t.TempDir()
	if dotenv != "" {
		if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(dotenv), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func TestLoadFromDotEnvWithDefaults(t *testing.T) {
	inDir(t, "# c\nTELEGRAM_BOT_TOKEN=1:abc\nENGINE_KEY = k-0123456789abcdef0123456789abcdef  # trailing\nexport ENGINE_URL=http://engine:8090/\n")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Token != "1:abc" || c.EngineURL != "http://engine:8090/" || c.EngineKey != "k-0123456789abcdef0123456789abcdef" || c.EngineTimeout != 150*time.Second {
		t.Fatalf("%+v", c)
	}
}

func TestEnvOverridesFileAndURLDefaults(t *testing.T) {
	inDir(t, "TELEGRAM_BOT_TOKEN=file\nENGINE_KEY=k\n")
	t.Setenv("TELEGRAM_BOT_TOKEN", "env")
	c, err := Load()
	if err != nil || c.Token != "env" || c.EngineURL != "http://127.0.0.1:8090" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestLoadValidation(t *testing.T) {
	for name, env := range map[string]string{
		"no token":        "ENGINE_KEY=k\n",
		"no key":          "TELEGRAM_BOT_TOKEN=t\n",
		"bad timeout":     "TELEGRAM_BOT_TOKEN=t\nENGINE_KEY=k\nENGINE_TIMEOUT=soon\n",
		"timeout too low": "TELEGRAM_BOT_TOKEN=t\nENGINE_KEY=k\nENGINE_TIMEOUT=5s\n",
		"bad url":         "TELEGRAM_BOT_TOKEN=t\nENGINE_KEY=k\nENGINE_URL=engine:8090\n",
	} {
		inDir(t, env)
		if _, err := Load(); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
