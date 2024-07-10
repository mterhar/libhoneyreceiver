package libhoneyreceiver

import (
	"errors"
	"fmt"
	"net/url"
	"path"

	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/confmap"
)

// Config represents the receiver config settings within the collector's config.yaml
type Config struct {
	HTTP       *HTTPConfig      `mapstructure:"http"`
	Wrapper    string           `mapstructure:"wrapper"`
	Resources  ResourcesConfig  `mapstructure:"resources"`
	Attributes AttributesConfig `mapstructure:"attributes"`
}

type HTTPConfig struct {
	*confighttp.ServerConfig `mapstructure:",squash"`

	// The URL path to receive traces on. If omitted "/" will be used.
	TracesURLPaths []string `mapstructure:"traces_url_paths,omitempty"`
}

type ResourcesConfig struct {
	ServiceName string `mapstructure:"service_name"`
}

type AttributesConfig struct {
	TraceId         string   `mapstructure:"trace_id"`
	ParentId        string   `mapstructure:"parent_id"`
	SpanId          string   `mapstructure:"span_id"`
	Name            string   `mapstructure:"name"`
	Error           string   `mapstructure:"error"`
	SpanKind        string   `mapstructure:"spankind"`
	StarttimeFields []string `mapstructure:"starttimeFields"`
	DurationFields  []string `mapstructure:"durationFields"`
	EndtimeFields   []string `mapstructure:"endtimeFields"`
}

func (cfg *Config) Validate() error {
	if cfg.HTTP == nil {
		return errors.New("must specify at least one protocol when using the arbitrary JSON receiver")
	}
	return nil
}

func (cfg *Config) Unmarshal(conf *confmap.Conf) error {
	// first load the config normally
	err := conf.Unmarshal(cfg)
	if err != nil {
		return err
	}

	if !conf.IsSet("http") {
		cfg.HTTP = nil
	} else {
		var err error

		for idx := range cfg.HTTP.TracesURLPaths {
			if cfg.HTTP.TracesURLPaths[idx], err = sanitizeURLPath(cfg.HTTP.TracesURLPaths[idx]); err != nil {
				return err
			}
		}
	}

	return nil
}

func sanitizeURLPath(urlPath string) (string, error) {
	u, err := url.Parse(urlPath)
	if err != nil {
		return "", fmt.Errorf("invalid HTTP URL path set for signal: %w", err)
	}

	if !path.IsAbs(u.Path) {
		u.Path = "/" + u.Path
	}
	return u.Path, nil
}
