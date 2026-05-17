package config

type LibraryMapping struct {
	RemoteName string `mapstructure:"remote_name"`
	LocalPath  string `mapstructure:"local_path"`
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
