package config

import (
	"errors"
	"fmt"

	"github.com/spf13/viper"
)

type Config struct {
	Target           TargetConfig   `mapstructure:"target"`
	Remotes          []RemoteConfig `mapstructure:"remotes"`
	TieBreakerFields []TieBreaker   `mapstructure:"tie_breaker_fields"`
	PollInterval     string         `mapstructure:"poll_interval"`
	WebhookPort      int            `mapstructure:"webhook_port"`
	DBPath           string         `mapstructure:"db_path"`
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config is nil")
	}
	if err := c.Target.Validate(); err != nil {
		return err
	}
	if len(c.GetRemotes()) == 0 {
		return errors.New("at least one remote is required")
	}
	return nil
}

func (c *Config) GetTarget() *TargetConfig {
	if c == nil {
		return nil
	}
	return &c.Target
}

func (c *Config) GetRemotes() []RemoteConfig {
	if c == nil {
		return nil
	}
	return c.Remotes
}

func (c *Config) GetTieBreakerFields() []TieBreaker {
	if c == nil {
		return nil
	}
	return c.TieBreakerFields
}

func (c *Config) GetPollInterval() string {
	if c == nil {
		return ""
	}
	return c.PollInterval
}

func (c *Config) GetWebhookPort() int {
	if c == nil {
		return 0
	}
	return c.WebhookPort
}

func (c *Config) GetDBPath() string {
	if c == nil {
		return ""
	}
	return c.DBPath
}

type RemoteConfig struct {
	ID              string           `mapstructure:"id"`
	Name            string           `mapstructure:"name"`
	APIURL          string           `mapstructure:"api_url"`
	StrmURL         string           `mapstructure:"strm_url"`
	APIKey          string           `mapstructure:"api_key"`
	RootStart       string           `mapstructure:"root_start"`
	LibraryMappings []LibraryMapping `mapstructure:"library_mappings"`
}

func (r *RemoteConfig) GetID() string {
	if r == nil {
		return ""
	}
	return r.ID
}

func (r *RemoteConfig) GetName() string {
	if r == nil {
		return ""
	}
	return r.Name
}

func (r *RemoteConfig) GetAPIURL() string {
	if r == nil {
		return ""
	}
	return r.APIURL
}

func (r *RemoteConfig) GetStrmURL() string {
	if r == nil {
		return ""
	}
	return r.StrmURL
}

func (r *RemoteConfig) GetAPIKey() string {
	if r == nil {
		return ""
	}
	return r.APIKey
}

func (r *RemoteConfig) GetRootStart() string {
	if r == nil {
		return ""
	}
	return r.RootStart
}

func (r *RemoteConfig) GetLibraryMappings() []LibraryMapping {
	if r == nil {
		return nil
	}
	return r.LibraryMappings
}

type TieBreaker struct {
	Field string `mapstructure:"field"`
	Value string `mapstructure:"value"`
}

func (tb *TieBreaker) GetField() string {
	if tb == nil {
		return ""
	}
	return tb.Field
}

func (tb *TieBreaker) GetValue() string {
	if tb == nil {
		return ""
	}
	return tb.Value
}

func Load(cfgFile string) (*Config, error) {
	v := viper.New()
	v.SetEnvPrefix("JellySynnc")
	v.AutomaticEnv()

	v.SetDefault("poll_interval", "15m")
	v.SetDefault("webhook_port", 8080)
	v.SetDefault("db_path", "JellySynnc.db")

	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("reading config: %w", err)
		}
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}
