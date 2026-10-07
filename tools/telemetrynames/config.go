package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Config is .agents/telemetry-docs.json: where to look for code, the metric
// prefixes the programs put before every metric tail, and which spec must
// list each log area ("enroll.") and each metric tail prefix ("writer_").
type Config struct {
	Version        int               `json:"version"`
	Note           string            `json:"note"`
	CodeDirs       []string          `json:"code_dirs"`
	MetricPrefixes []string          `json:"metric_prefixes"`
	Logs           map[string]string `json:"logs"`
	Metrics        map[string]string `json:"metrics"`
}

// LoadConfig reads and validates the name map. Unknown fields are refused so
// a misspelled key is not silently ignored.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is the repo's own name map (-config, default .agents/telemetry-docs.json)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	var errs []error
	if c.Version != 1 {
		errs = append(errs, fmt.Errorf("config version is %d, want 1", c.Version))
	}
	if len(c.CodeDirs) == 0 {
		errs = append(errs, errors.New("config has no code_dirs"))
	}
	for _, p := range c.MetricPrefixes {
		if p == "" || strings.HasSuffix(p, "_") {
			errs = append(errs, fmt.Errorf("metric prefix %q must be non-empty without a trailing _", p))
		}
	}
	if len(c.MetricPrefixes) == 0 {
		errs = append(errs, errors.New("config has no metric_prefixes"))
	}
	for key, spec := range c.Logs {
		area, ok := strings.CutSuffix(key, ".")
		if !ok || area == "" || strings.Contains(area, ".") {
			errs = append(errs, fmt.Errorf("log key %q must be one area followed by a dot, like \"hub.\"", key))
		}
		errs = append(errs, checkSpecPath(key, spec))
	}
	for key, spec := range c.Metrics {
		if key == "" {
			errs = append(errs, errors.New("metric key must be non-empty"))
		}
		errs = append(errs, checkSpecPath(key, spec))
	}
	return errors.Join(errs...)
}

// checkSpecPath keeps the source of truth in the specs, never a README.
func checkSpecPath(key, spec string) error {
	if !strings.HasPrefix(spec, "docs/specs/") || !strings.HasSuffix(spec, ".md") || strings.Contains(spec, "..") {
		return fmt.Errorf("key %q maps to %q; it must be a spec under docs/specs/", key, spec)
	}
	return nil
}

// specs is every spec the config names, sorted.
func (c Config) specs() []string {
	seen := map[string]bool{}
	for _, s := range c.Logs {
		seen[s] = true
	}
	for _, s := range c.Metrics {
		seen[s] = true
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// logSpec is the spec for a log event's area ("" when unmapped).
func (c Config) logSpec(event string) (area, spec string) {
	i := strings.Index(event, ".")
	if i <= 0 {
		return "", ""
	}
	area = event[:i+1]
	return area, c.Logs[area]
}

// metricKey is a metric's name after "<prefix>_", or the whole name when no
// prefix matches.
func (c Config) metricKey(full string) string {
	for _, p := range c.MetricPrefixes {
		if rest, ok := strings.CutPrefix(full, p+"_"); ok {
			return rest
		}
	}
	return full
}

// metricSpec is the spec of the longest metric key that prefixes key.
func (c Config) metricSpec(key string) (prefix, spec string) {
	for k, s := range c.Metrics {
		if strings.HasPrefix(key, k) && len(k) > len(prefix) {
			prefix, spec = k, s
		}
	}
	return prefix, spec
}
