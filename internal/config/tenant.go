package config

// CloudConfig cloud connection configuration (loaded from configs/goteams-client/config.ini)
type CloudConfig struct {
	APIBaseURL    string // Cloud API address
	ClientVersion string //Client version number
	ListenAddr    string //Local service listening address (the address opened by the browser)
	iniPath       string // INI configuration file path (for runtime reading and writing)
	configSource  string // The actual effective configuration source (embedded config_<env>.ini or the path covered by the disk), used for log display

	// DocumentUpload 产出文档上报配置（[document-upload] 段）
	DocumentUpload DocumentUploadConfig
}

// NewCloudConfig creates a cloud configuration instance
func NewCloudConfig(apiBaseURL, clientVersion, iniPath string) *CloudConfig {
	return &CloudConfig{
		APIBaseURL:     apiBaseURL,
		ClientVersion:  clientVersion,
		iniPath:        iniPath,
		DocumentUpload: DefaultDocumentUploadConfig(),
	}
}

// INIPath returns the configuration file path (read and write disk target during runtime)
func (c *CloudConfig) INIPath() string {
	return c.iniPath
}

// ConfigSource returns the actual effective configuration source path (embedded config_<env>.ini or path overwritten by disk)
func (c *CloudConfig) ConfigSource() string {
	return c.configSource
}

// SetConfigSource sets the actual effective configuration source path
func (c *CloudConfig) SetConfigSource(source string) {
	c.configSource = source
}
