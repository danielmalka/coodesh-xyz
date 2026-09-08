package lexi

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"time"

	"golang.org/x/time/rate"
)

const (
	defaultAddr            = ":8080"
	defaultWorkers         = 4
	defaultQueueSize       = 256
	defaultLLMTimeout      = 5 * time.Second
	defaultMaxAttempts     = 3
	defaultWhatsAppURL     = "http://127.0.0.1:8080/mock/whatsapp/messages"
	defaultInboundRPS      = 50
	defaultInboundBurst    = 100
	defaultUpstreamRPS     = 10
	defaultUpstreamBurst   = 10
	defaultShutdownTimeout = 10 * time.Second
	defaultLLMMinLatency   = 200 * time.Millisecond
	defaultLLMMaxLatency   = 2 * time.Second
	defaultLLMFailureRate  = 0.15
	defaultLLMSlowRate     = 0.10
	defaultRetryBaseDelay  = 200 * time.Millisecond
	defaultRetryMaxDelay   = 2 * time.Second
	defaultOutboundTimeout = 5 * time.Second
	defaultMockFailureRate = 0
	minRate                = 0
	maxRate                = 1
	maxLLMLatency          = time.Hour
	retryAfterMin          = time.Second
)

type Config struct {
	Addr                    string
	Workers                 int
	QueueSize               int
	LLMTimeout              time.Duration
	MaxAttempts             int
	WhatsAppURL             string
	WhatsAppToken           string
	InboundRPS              float64
	InboundBurst            int
	UpstreamRPS             float64
	UpstreamBurst           int
	ShutdownTimeout         time.Duration
	LLMMinLatency           time.Duration
	LLMMaxLatency           time.Duration
	LLMFailureRate          float64
	LLMSlowRate             float64
	RetryBaseDelay          time.Duration
	RetryMaxDelay           time.Duration
	OutboundTimeout         time.Duration
	MockWhatsAppFailureRate float64
}

func DefaultConfig() Config {
	return Config{
		Addr:                    defaultAddr,
		Workers:                 defaultWorkers,
		QueueSize:               defaultQueueSize,
		LLMTimeout:              defaultLLMTimeout,
		MaxAttempts:             defaultMaxAttempts,
		WhatsAppURL:             defaultWhatsAppURL,
		InboundRPS:              defaultInboundRPS,
		InboundBurst:            defaultInboundBurst,
		UpstreamRPS:             defaultUpstreamRPS,
		UpstreamBurst:           defaultUpstreamBurst,
		ShutdownTimeout:         defaultShutdownTimeout,
		LLMMinLatency:           defaultLLMMinLatency,
		LLMMaxLatency:           defaultLLMMaxLatency,
		LLMFailureRate:          defaultLLMFailureRate,
		LLMSlowRate:             defaultLLMSlowRate,
		RetryBaseDelay:          defaultRetryBaseDelay,
		RetryMaxDelay:           defaultRetryMaxDelay,
		OutboundTimeout:         defaultOutboundTimeout,
		MockWhatsAppFailureRate: defaultMockFailureRate,
	}
}

func (c Config) LLMSimulation() LLMSimulation {
	return LLMSimulation{
		MinLatency:  c.LLMMinLatency,
		MaxLatency:  c.LLMMaxLatency,
		FailureRate: c.LLMFailureRate,
		SlowRate:    c.LLMSlowRate,
	}
}

func (c Config) retryPolicy() retryPolicy {
	return retryPolicy{maxAttempts: c.MaxAttempts, baseDelay: c.RetryBaseDelay, maxDelay: c.RetryMaxDelay}
}

func (c Config) inboundLimiter() *rate.Limiter {
	return rate.NewLimiter(rate.Limit(c.InboundRPS), c.InboundBurst)
}

func (c Config) upstreamLimiter() *rate.Limiter {
	return rate.NewLimiter(rate.Limit(c.UpstreamRPS), c.UpstreamBurst)
}

func (c Config) retryAfterSeconds() int {
	sec := int(math.Ceil(1 / c.InboundRPS))
	if d := time.Duration(sec) * time.Second; d < retryAfterMin {
		return int(retryAfterMin / time.Second)
	}
	return sec
}

func ConfigFromEnv() (Config, error) {
	cfg := DefaultConfig()
	var errs []error

	readString("ADDR", &cfg.Addr)
	readString("WHATSAPP_API_URL", &cfg.WhatsAppURL)
	readString("WHATSAPP_TOKEN", &cfg.WhatsAppToken)
	errs = append(errs,
		readInt("WORKERS", &cfg.Workers),
		readInt("QUEUE_SIZE", &cfg.QueueSize),
		readInt("MAX_ATTEMPTS", &cfg.MaxAttempts),
		readFloat("INBOUND_RPS", &cfg.InboundRPS),
		readInt("INBOUND_BURST", &cfg.InboundBurst),
		readFloat("UPSTREAM_RPS", &cfg.UpstreamRPS),
		readInt("UPSTREAM_BURST", &cfg.UpstreamBurst),
		readDuration("LLM_TIMEOUT", &cfg.LLMTimeout),
		readDuration("SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout),
		readDuration("LLM_MIN_LATENCY", &cfg.LLMMinLatency),
		readDuration("LLM_MAX_LATENCY", &cfg.LLMMaxLatency),
		readFloat("LLM_FAILURE_RATE", &cfg.LLMFailureRate),
		readFloat("LLM_SLOW_RATE", &cfg.LLMSlowRate),
		readDuration("RETRY_BASE_DELAY", &cfg.RetryBaseDelay),
		readDuration("RETRY_MAX_DELAY", &cfg.RetryMaxDelay),
		readDuration("OUTBOUND_TIMEOUT", &cfg.OutboundTimeout),
		readFloat("MOCK_WHATSAPP_FAILURE_RATE", &cfg.MockWhatsAppFailureRate),
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
	if c.InboundRPS <= 0 || math.IsNaN(c.InboundRPS) || math.IsInf(c.InboundRPS, 0) {
		errs = append(errs, errors.New("INBOUND_RPS must be > 0"))
	}
	if c.InboundBurst < 1 {
		errs = append(errs, errors.New("INBOUND_BURST must be >= 1"))
	}
	if c.UpstreamRPS <= 0 || math.IsNaN(c.UpstreamRPS) || math.IsInf(c.UpstreamRPS, 0) {
		errs = append(errs, errors.New("UPSTREAM_RPS must be > 0"))
	}
	if c.UpstreamBurst < 1 {
		errs = append(errs, errors.New("UPSTREAM_BURST must be >= 1"))
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
	if c.LLMMinLatency < 0 {
		errs = append(errs, errors.New("LLM_MIN_LATENCY must be >= 0"))
	}
	if c.LLMMaxLatency < 0 {
		errs = append(errs, errors.New("LLM_MAX_LATENCY must be >= 0"))
	}
	if c.LLMMaxLatency > maxLLMLatency {
		errs = append(errs, errors.New("LLM_MAX_LATENCY must be <= 1h"))
	}
	if c.LLMMinLatency > c.LLMMaxLatency {
		errs = append(errs, errors.New("LLM_MIN_LATENCY must be <= LLM_MAX_LATENCY"))
	}
	if c.LLMFailureRate < minRate || c.LLMFailureRate > maxRate {
		errs = append(errs, errors.New("LLM_FAILURE_RATE must be in [0,1]"))
	}
	if c.LLMSlowRate < minRate || c.LLMSlowRate > maxRate {
		errs = append(errs, errors.New("LLM_SLOW_RATE must be in [0,1]"))
	}
	if c.LLMFailureRate+c.LLMSlowRate > maxRate {
		errs = append(errs, errors.New("LLM_FAILURE_RATE + LLM_SLOW_RATE must be <= 1"))
	}
	if c.RetryBaseDelay <= 0 {
		errs = append(errs, errors.New("RETRY_BASE_DELAY must be > 0"))
	}
	if c.RetryMaxDelay <= 0 {
		errs = append(errs, errors.New("RETRY_MAX_DELAY must be > 0"))
	}
	if c.OutboundTimeout <= 0 {
		errs = append(errs, errors.New("OUTBOUND_TIMEOUT must be > 0"))
	}
	if c.RetryBaseDelay > 0 && c.RetryMaxDelay > 0 && c.RetryBaseDelay > c.RetryMaxDelay {
		errs = append(errs, errors.New("RETRY_BASE_DELAY must be <= RETRY_MAX_DELAY"))
	}
	if c.MockWhatsAppFailureRate < minRate || c.MockWhatsAppFailureRate > maxRate {
		errs = append(errs, errors.New("MOCK_WHATSAPP_FAILURE_RATE must be in [0,1]"))
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
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return fmt.Errorf("%s: must be a finite number", key)
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
