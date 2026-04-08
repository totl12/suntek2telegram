package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Telegram  *Telegram  `yaml:"telegram"`
	FTP       *FTPServer `yaml:"ftp"`
	SMTP      *SMTPServer `yaml:"smtp"`
	Web       *Web       `yaml:"web"`
	DBPath    string     `yaml:"db_path"`
	PhotosDir string     `yaml:"photos_dir"`
}

type Telegram struct {
	APIKey string `yaml:"api_key"`
}

// FTPServer is the single shared FTP server. Traps are identified by their credentials.
type FTPServer struct {
	Enabled      bool   `yaml:"enabled"`
	BindHost     string `yaml:"bind_host"`
	BindPort     int    `yaml:"bind_port"`
	PublicIP     string `yaml:"public_ip"`
	PassivePorts string `yaml:"passive_ports"`
}

// SMTPServer is the single shared SMTP server. Traps are identified by their credentials.
type SMTPServer struct {
	Enabled  bool   `yaml:"enabled"`
	BindHost string `yaml:"bind_host"`
	BindPort int    `yaml:"bind_port"`
}

type Web struct {
	BindHost string `yaml:"bind_host"`
	BindPort int    `yaml:"bind_port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

func New(path string) (*Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Defaults
	c := &Config{
		FTP:       &FTPServer{BindHost: "0.0.0.0", BindPort: 21},
		SMTP:      &SMTPServer{BindHost: "0.0.0.0", BindPort: 25},
		Web:       &Web{BindHost: "0.0.0.0", BindPort: 8080},
		DBPath:    "./data/suntek.db",
		PhotosDir: "./data/photos",
	}

	if err := yaml.Unmarshal(contents, c); err != nil {
		return nil, err
	}

	return validateConfig(c)
}
