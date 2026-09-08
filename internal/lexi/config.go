package lexi

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr            string
	Workers         int
	QueueSize       int
	LLMTimeout      time.Duration
	MaxAttempts     int
	WhatsAppURL     string
	RateLimitRPS    float64
	ShutdownTimeout time.Duration
}

func DefaultConfig() Config {
	return Config{
		Addr:            ":8080",
		Workers:         4,
		QueueSize:       256,
		LLMTimeout:      5 * time.Second,
		MaxAttempts:     3,
		WhatsAppURL:     "http://127.0.0.1:8080/mock/whatsapp/messages",
		RateLimitRPS:    50,
		ShutdownTimeout: 10 * time.Second,
	}
}

func ConfigFromEnv() (Config, error) {
	cfg := DefaultConfig()
	var errs []error

	readString("ADDR", &cfg.Addr)
	readString("WHATSAPP_API_URL", &cfg.WhatsAppURL)
	errs = append(errs,
		readInt("WORKERS", &cfg.Workers),
		readInt("QUEUE_SIZE", &cfg.QueueSize),
		readInt("MAX_ATTEMPTS", &cfg.MaxAttempts),
		readFloat("RATE_LIMIT_RPS", &cfg.RateLimitRPS),
		readDuration("LLM_TIMEOUT", &cfg.LLMTimeout),
		readDuration("SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout),
	)
	if err := errors.Join(errs...); err != nil {
		return Config{}, err
	}
	return cfg, cfg.validate()
}

func (c Config) validate() error {
	var errs []error
	if c.Addr == "" {
		errs = append(errs, errors.New("ADDR must not be empty"))
	}
	if c.Workers < 1 {
		errs = append(errs, errors.New("WORKERS must be >= 1"))
	}
	if c.QueueSize < 1 {
		errs = append(errs, errors.New("QUEUE_SIZE must be >= 1"))
	}
	if c.MaxAttempts < 1 {
		errs = append(errs, errors.New("MAX_ATTEMPTS must be >= 1"))
	}
	if c.RateLimitRPS <= 0 {
		errs = append(errs, errors.New("RATE_LIMIT_RPS must be > 0"))
	}
	if c.LLMTimeout <= 0 {
		errs = append(errs, errors.New("LLM_TIMEOUT must be > 0"))
	}
	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("SHUTDOWN_TIMEOUT must be > 0"))
	}
	if u, err := url.Parse(c.WhatsAppURL); err != nil || u.Scheme == "" || u.Host == "" {
		errs = append(errs, errors.New("WHATSAPP_API_URL must be an absolute http(s) URL"))
	}
	return errors.Join(errs...)
}

func readString(key string, dst *string) {
	if v, ok := os.LookupEnv(key); ok {
		*dst = v
	}
}

func readInt(key string, dst *int) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = n
	return nil
}

func readFloat(key string, dst *float64) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = f
	return nil
}

func readDuration(key string, dst *time.Duration) error {
	v, ok := os.LookupEnv(key)
	if !ok {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = d
	return nil
}
