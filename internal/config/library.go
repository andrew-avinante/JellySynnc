package config

type LibraryMapping struct {
	RemoteName             string `mapstructure:"remote_name"`
	LocalPath              string `mapstructure:"local_path"`
	SyncUnknownProviderIDs bool   `mapstructure:"sync_unknown_provider_ids"`
}

func (l *LibraryMapping) GetRemoteName() string {
	if l == nil {
		return ""
	}
	return l.RemoteName
}

func (l *LibraryMapping) GetLocalPath() string {
	if l == nil {
		return ""
	}
	return l.LocalPath
}

func (l *LibraryMapping) GetSyncUnknownProviderIDs() bool {
	if l == nil {
		return false
	}
	return l.SyncUnknownProviderIDs
}
