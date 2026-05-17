package config

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
