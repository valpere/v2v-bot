// Package config loads the bot's settings from the environment, falling back to
// a .env file in the working directory for keys that are unset or empty.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config is the bot's configuration.
type Config struct {
	Token         string        // TELEGRAM_BOT_TOKEN
	EngineURL     string        // ENGINE_URL, default http://127.0.0.1:8090
	EngineKey     string        // ENGINE_KEY: the engine's API_KEY
	EngineTimeout time.Duration // ENGINE_TIMEOUT, default 150s: must exceed the engine's TURN_TIMEOUT (120s)
}

// minTimeout guards against a timeout that would cut healthy turns short.
const minTimeout = 30 * time.Second

// Load reads and validates the configuration.
func Load() (Config, error) {
	file, err := readDotEnv(".env")
	if err != nil {
		return Config{}, err
	}
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		if v := file[k]; v != "" {
			return v
		}
		return def
	}
	c := Config{
		Token:         get("TELEGRAM_BOT_TOKEN", ""),
		EngineURL:     get("ENGINE_URL", "http://127.0.0.1:8090"),
		EngineKey:     get("ENGINE_KEY", ""),
		EngineTimeout: 150 * time.Second,
	}
	var errs []string
	if v := get("ENGINE_TIMEOUT", ""); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			errs = append(errs, fmt.Sprintf("ENGINE_TIMEOUT %q: %v", v, err))
		case d < minTimeout:
			errs = append(errs, fmt.Sprintf("ENGINE_TIMEOUT %s is below %s — it must exceed the engine's TURN_TIMEOUT", d, minTimeout))
		default:
			c.EngineTimeout = d
		}
	}
	if c.Token == "" {
		errs = append(errs, "TELEGRAM_BOT_TOKEN is required")
	}
	if c.EngineKey == "" {
		errs = append(errs, "ENGINE_KEY is required")
	}
	if u, err := url.Parse(c.EngineURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		errs = append(errs, fmt.Sprintf("ENGINE_URL %q must be an http(s) URL", c.EngineURL))
	}
	if len(errs) > 0 {
		return Config{}, errors.New(strings.Join(errs, "; "))
	}
	return c, nil
}

// readDotEnv parses KEY=VALUE lines (comments, blank lines, `export ` and
// trailing ` # comment` tolerated). A missing file is not an error.
func readDotEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s: malformed line %q", path, line)
		}
		if i := strings.Index(v, " #"); i >= 0 {
			v = v[:i]
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out, sc.Err()
}
