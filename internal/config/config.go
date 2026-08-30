package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	defaultAPIURL       = "https://flare.am"
	keyringService      = "flare-cli"
	tokenBackendFile    = "file"
	tokenBackendKeyring = "keyring"
)

var (
	keyringGet    = keyring.Get
	keyringSet    = keyring.Set
	keyringDelete = keyring.Delete
)

type Config struct {
	APIURL       string `json:"api_url"`
	Token        string `json:"token,omitempty"`
	TokenBackend string `json:"token_backend,omitempty"`
	TokenAPIURL  string `json:"token_api_url,omitempty"`
}

func Load(overrideURL string) (*Config, error) {
	config := &Config{APIURL: defaultAPIURL}
	migratedLegacyToken := false
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if config.Token != "" {
			if config.TokenBackend == "" {
				config.TokenBackend = tokenBackendFile
				migratedLegacyToken = true
			}
			if config.TokenAPIURL == "" {
				config.TokenAPIURL = strings.TrimRight(config.APIURL, "/")
				migratedLegacyToken = true
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read config: %w", err)
	}

	envURL := os.Getenv("FLARE_API_URL")
	if envURL != "" {
		config.APIURL = envURL
	}
	if overrideURL != "" {
		config.APIURL = overrideURL
	}
	config.APIURL = strings.TrimRight(config.APIURL, "/")
	if err := validateAPIURL(config.APIURL); err != nil {
		return nil, fmt.Errorf("invalid API URL: %w", err)
	}
	if migratedLegacyToken && envURL == "" && overrideURL == "" {
		if err := config.Save(); err != nil {
			return nil, fmt.Errorf("migrate legacy token config: %w", err)
		}
	}
	return config, nil
}

func validateAPIURL(value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return err
	}
	if parsed.Scheme == "https" && parsed.Host != "" {
		return nil
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if parsed.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("use HTTPS, or HTTP only for a loopback host")
}

func (config *Config) Save() error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func Path() (string, error) {
	directory, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find config directory: %w", err)
	}
	return filepath.Join(directory, "flare", "config.json"), nil
}

func (config *Config) TokenValue() (string, error) {
	if token := os.Getenv("FLARE_TOKEN"); token != "" {
		return token, nil
	}
	if config.TokenBackend == tokenBackendFile {
		if config.Token != "" && config.TokenAPIURL == config.APIURL {
			return config.Token, nil
		}
		return "", fmt.Errorf("no stored credentials for %s; run `flare auth login` or set FLARE_TOKEN", config.APIURL)
	}
	token, err := keyringGet(keyringService, config.APIURL)
	if err == nil {
		return token, nil
	}
	if config.TokenBackend == "" && config.Token != "" {
		return config.Token, nil
	}
	return "", errors.New("not authenticated; run `flare auth login`")
}

// StoreToken prefers the operating system credential store. On systems where
// it is unavailable, it falls back to the mode-0600 config file and tells the
// caller so it can display a warning.
func (config *Config) StoreToken(token string) (fallback bool, err error) {
	if err := keyringSet(keyringService, config.APIURL, token); err == nil {
		config.Token = ""
		config.TokenBackend = tokenBackendKeyring
		config.TokenAPIURL = ""
		return false, config.Save()
	}
	config.Token = token
	config.TokenBackend = tokenBackendFile
	config.TokenAPIURL = config.APIURL
	return true, config.Save()
}

func (config *Config) DeleteToken() error {
	keyringErr := keyringDelete(keyringService, config.APIURL)
	config.Token = ""
	config.TokenBackend = ""
	config.TokenAPIURL = ""
	if err := config.Save(); err != nil {
		return err
	}
	if keyringErr != nil && !errors.Is(keyringErr, keyring.ErrNotFound) {
		return fmt.Errorf("remove token from OS credential store: %w", keyringErr)
	}
	return nil
}
