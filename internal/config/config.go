package config

import (
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

type TargetConfig struct {
	Name   string `mapstructure:"name"`
	URL    string `mapstructure:"url"`
	APIKey string `mapstructure:"api_key"`
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

type LibraryMapping struct {
	RemoteName string `mapstructure:"remote_name"`
	LocalPath  string `mapstructure:"local_path"`
}

type TieBreaker struct {
	Field string `mapstructure:"field"`
	Value string `mapstructure:"value"`
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

	if cfg.Target.URL == "" {
		return nil, fmt.Errorf("target.url is required")
	}
	if cfg.Target.APIKey == "" {
		return nil, fmt.Errorf("target.api_key is required")
	}
	if len(cfg.Remotes) == 0 {
		return nil, fmt.Errorf("at least one remote is required")
	}

	return &cfg, nil
}
