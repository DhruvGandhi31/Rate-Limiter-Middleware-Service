package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Algorithm string

const (
	TokenBucket   Algorithm = "token_bucket"
	FixedWindow   Algorithm = "fixed_window"
	SlidingWindow Algorithm = "sliding_window"
)

type FailMode string

const (
	FailOpen   FailMode = "open"
	FailClosed FailMode = "closed"
)

// Rule defines a single rate-limit rule. Identifier selects the bucket key
// (api_key, ip, or endpoint), and the algorithm/limit pair controls behavior.
type Rule struct {
	Name       string        `yaml:"name"`
	Identifier string        `yaml:"identifier"` // api_key | ip | endpoint
	Match      string        `yaml:"match"`      // regex/glob for endpoint, or empty for all
	Algorithm  Algorithm     `yaml:"algorithm"`
	Limit      int64         `yaml:"limit"`      // requests per window (fixed/sliding) or bucket capacity (token)
	Window     time.Duration `yaml:"window"`     // window duration (fixed/sliding) or refill period (token)
	RefillRate float64       `yaml:"refill_rate"` // token bucket: tokens per second
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	PoolSize int    `yaml:"pool_size"`
}

type ServerConfig struct {
	Addr            string        `yaml:"addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type LoggingConfig struct {
	// Level is one of "debug", "info", "warn", "error". Default: info.
	Level string `yaml:"level"`
	// Format is "json" (recommended for prod, parsable by Loki/Datadog) or "text".
	Format string `yaml:"format"`
}

type AdminConfig struct {
	// Token is the shared secret callers send as `Authorization: Bearer <token>`.
	// Empty token disables auth (dev only — logged as a warning at startup).
	Token string `yaml:"token"`
}

type Config struct {
	Server   ServerConfig  `yaml:"server"`
	Redis    RedisConfig   `yaml:"redis"`
	FailMode FailMode      `yaml:"fail_mode"`
	Logging  LoggingConfig `yaml:"logging"`
	Admin    AdminConfig   `yaml:"admin"`
	Rules    []Rule        `yaml:"rules"`
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Addr:            ":8080",
			ReadTimeout:     5 * time.Second,
			WriteTimeout:    5 * time.Second,
			ShutdownTimeout: 10 * time.Second,
		},
		Redis: RedisConfig{
			Addr:     "localhost:6379",
			PoolSize: 50,
		},
		FailMode: FailClosed,
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Rules: []Rule{
			{
				Name:       "default",
				Identifier: "api_key",
				Algorithm:  TokenBucket,
				Limit:      100,
				Window:     time.Second,
				RefillRate: 10,
			},
		},
	}
}

func Load(path string) (*Config, error) {
	if path == "" {
		return applyEnv(Default()), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	return applyEnv(cfg), cfg.Validate()
}

func applyEnv(cfg *Config) *Config {
	if v := os.Getenv("REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := os.Getenv("REDIS_PASSWORD"); v != "" {
		cfg.Redis.Password = v
	}
	if v := os.Getenv("SERVER_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("FAIL_MODE"); v != "" {
		cfg.FailMode = FailMode(v)
	}
	if v := os.Getenv("ADMIN_TOKEN"); v != "" {
		cfg.Admin.Token = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Logging.Level = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.Logging.Format = v
	}
	return cfg
}

func (c *Config) Validate() error {
	if c.FailMode != FailOpen && c.FailMode != FailClosed {
		return fmt.Errorf("fail_mode must be 'open' or 'closed', got %q", c.FailMode)
	}
	for i, r := range c.Rules {
		if r.Limit <= 0 {
			return fmt.Errorf("rule[%d] %q: limit must be > 0", i, r.Name)
		}
		switch r.Algorithm {
		case TokenBucket:
			if r.RefillRate <= 0 {
				return fmt.Errorf("rule[%d] %q: token_bucket requires refill_rate > 0", i, r.Name)
			}
		case FixedWindow, SlidingWindow:
			if r.Window <= 0 {
				return fmt.Errorf("rule[%d] %q: %s requires window > 0", i, r.Name, r.Algorithm)
			}
		default:
			return fmt.Errorf("rule[%d] %q: unknown algorithm %q", i, r.Name, r.Algorithm)
		}
	}
	return nil
}
