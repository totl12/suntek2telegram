package config

import "errors"

func validateConfig(c *Config) (*Config, error) {
	if c.Telegram == nil || c.Telegram.APIKey == "" {
		return nil, errors.New("telegram.api_key is required")
	}
	if c.Web != nil && (c.Web.Username == "") != (c.Web.Password == "") {
		return nil, errors.New("web.username and web.password must both be set or both be empty")
	}
	return c, nil
}
