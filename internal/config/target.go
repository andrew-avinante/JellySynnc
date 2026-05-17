package config

import "errors"

type TargetConfig struct {
	Name   string `mapstructure:"name"`
	URL    string `mapstructure:"url"`
	APIKey string `mapstructure:"api_key"`
}

func (t *TargetConfig) Validate() error {
	if t == nil {
		return errors.New("target config is nil")
	}
	if t.URL == "" {
		return errors.New("target.url is required")
	}
	if t.APIKey == "" {
		return errors.New("target.api_key is required")
	}
	return nil
}

func (t *TargetConfig) GetName() string {
	if t == nil {
		return ""
	}
	return t.Name
}

func (t *TargetConfig) GetURL() string {
	if t == nil {
		return ""
	}
	return t.URL
}

func (t *TargetConfig) GetAPIKey() string {
	if t == nil {
		return ""
	}
	return t.APIKey
}
