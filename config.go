package cloud

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Config represents stored credentials compatible with rmapi.
type Config struct {
	DeviceToken string `json:"devicetoken"`
	UserToken   string `json:"usertoken"`
}

// ParseConfig parses rmapi-compatible key-value configuration from a reader.
// Format:
//
//	devicetoken: <str>
//	usertoken: <str>
func ParseConfig(r io.Reader) (*Config, error) {
	cfg := &Config{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		val := strings.TrimSpace(parts[1])
		switch key {
		case "devicetoken":
			cfg.DeviceToken = val
		case "usertoken":
			cfg.UserToken = val
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	return cfg, nil
}

// WriteConfig writes the configuration in rmapi key-value format to a writer.
func WriteConfig(w io.Writer, cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("%w: nil config", ErrInvalidConfig)
	}
	content := fmt.Sprintf("devicetoken: %s\nusertoken: %s\n", cfg.DeviceToken, cfg.UserToken)
	_, err := io.WriteString(w, content)
	return err
}

// CandidateConfigPaths returns the ordered list of standard rmapi configuration paths.
func CandidateConfigPaths() []string {
	var paths []string

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	rmapiConfig := os.Getenv("RMAPI_CONFIG")
	if rmapiConfig != "" {
		if fi, err := os.Stat(rmapiConfig); err == nil && !fi.IsDir() {
			paths = append(paths, rmapiConfig)
		} else {
			paths = append(paths, filepath.Join(rmapiConfig, ".rmapi"))
			paths = append(paths, filepath.Join(rmapiConfig, ".rmapi.conf"))
			paths = append(paths, filepath.Join(rmapiConfig, "rmapi", ".rmapi"))
			paths = append(paths, filepath.Join(rmapiConfig, "rmapi", "rmapi.conf"))
		}
	}

	if home != "" {
		paths = append(paths, filepath.Join(home, ".rmapi"))
		paths = append(paths, filepath.Join(home, ".rmapi.conf"))
	}

	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig != "" {
		paths = append(paths, filepath.Join(xdgConfig, "rmapi", "rmapi.conf"))
		paths = append(paths, filepath.Join(xdgConfig, "rmapi", ".rmapi"))
	} else if home != "" {
		paths = append(paths, filepath.Join(home, ".config", "rmapi", "rmapi.conf"))
		paths = append(paths, filepath.Join(home, ".config", "rmapi", ".rmapi"))
	}

	if home != "" {
		paths = append(paths, filepath.Join(home, "Library", "Application Support", "rmapi", "rmapi.conf"))
		paths = append(paths, filepath.Join(home, "Library", "Application Support", "rmapi", ".rmapi"))
	}

	return paths
}

// ResolveConfigPath resolves the configuration file path. If override is non-empty,
// that path is returned. Otherwise, candidate paths are checked sequentially for existence.
// If none exist, the default ~/.rmapi path is returned.
func ResolveConfigPath(override string) (string, error) {
	if override != "" {
		return filepath.Clean(override), nil
	}
	candidates := CandidateConfigPaths()
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c, nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("unable to determine home directory: %w", err)
	}
	return filepath.Join(home, ".rmapi"), nil
}

// ReadConfigFile reads and parses an rmapi configuration file from path.
func ReadConfigFile(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrConfigNotFound, path)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseConfig(f)
}

// WriteConfigFile writes the configuration to disk with restricted permissions (0600).
func WriteConfigFile(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmpFile := path + ".tmp"
	f, err := os.OpenFile(tmpFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err := WriteConfig(f, cfg); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpFile)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	return os.Rename(tmpFile, path)
}

// LoadConfig resolves the configuration file path and loads it.
// Returns the parsed Config and the path from which it was loaded.
func LoadConfig(override string) (*Config, string, error) {
	path, err := ResolveConfigPath(override)
	if err != nil {
		return nil, "", err
	}
	cfg, err := ReadConfigFile(path)
	if err != nil {
		return nil, path, err
	}
	return cfg, path, nil
}
