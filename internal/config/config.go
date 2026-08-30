package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	defaultAPIURL  = "https://flare.am"
	keyringService = "flare-cli"
)

type Config struct {
	APIURL string `json:"api_url"`
	Token  string `json:"token,omitempty"`
}

func Load(overrideURL string) (*Config, error) {
	config := &Config{APIURL: defaultAPIURL}
	path, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, config); err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read config: %w", err)
	}

	if envURL := os.Getenv("FLARE_API_URL"); envURL != "" {
		config.APIURL = envURL
	}
	if overrideURL != "" {
		config.APIURL = overrideURL
	}
	config.APIURL = strings.TrimRight(config.APIURL, "/")
	if _, err := url.ParseRequestURI(config.APIURL); err != nil {
		return nil, fmt.Errorf("invalid API URL: %w", err)
	}
	return config, nil
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
	token, err := keyring.Get(keyringService, config.APIURL)
	if err == nil {
		return token, nil
	}
	if config.Token != "" {
		return config.Token, nil
	}
	return "", errors.New("not authenticated; run `flare auth login`")
}

// StoreToken prefers the operating system credential store. On systems where
// it is unavailable, it falls back to the mode-0600 config file and tells the
// caller so it can display a warning.
func (config *Config) StoreToken(token string) (fallback bool, err error) {
	if err := keyring.Set(keyringService, config.APIURL, token); err == nil {
		config.Token = ""
		return false, config.Save()
	}
	config.Token = token
	return true, config.Save()
}

func (config *Config) DeleteToken() error {
	err := keyring.Delete(keyringService, config.APIURL)
	if err != nil && !errors.Is(err, keyring.ErrNotFound) {
		// Continue so a file fallback is still removed.
	}
	config.Token = ""
	return config.Save()
}
