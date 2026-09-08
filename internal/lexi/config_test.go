package lexi

import (
	"strings"
	"testing"
	"time"
)

func TestConfigFromEnvDefaults(t *testing.T) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg != DefaultConfig() {
		t.Fatalf("cfg = %+v, want defaults", cfg)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("ADDR", ":9090")
	t.Setenv("WORKERS", "8")
	t.Setenv("QUEUE_SIZE", "1024")
	t.Setenv("MAX_ATTEMPTS", "5")
	t.Setenv("RATE_LIMIT_RPS", "12.5")
	t.Setenv("LLM_TIMEOUT", "1500ms")
	t.Setenv("SHUTDOWN_TIMEOUT", "3s")
	t.Setenv("WHATSAPP_API_URL", "https://graph.facebook.com/v20.0/123/messages")

	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := Config{
		Addr:            ":9090",
		Workers:         8,
		QueueSize:       1024,
		LLMTimeout:      1500 * time.Millisecond,
		MaxAttempts:     5,
		WhatsAppURL:     "https://graph.facebook.com/v20.0/123/messages",
		RateLimitRPS:    12.5,
		ShutdownTimeout: 3 * time.Second,
	}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestConfigFromEnvRejectsBadValues(t *testing.T) {
	cases := map[string]struct{ key, value, wantErr string }{
		"non numeric workers": {"WORKERS", "many", "WORKERS"},
		"zero workers":        {"WORKERS", "0", "WORKERS must be >= 1"},
		"zero queue":          {"QUEUE_SIZE", "0", "QUEUE_SIZE must be >= 1"},
		"zero attempts":       {"MAX_ATTEMPTS", "0", "MAX_ATTEMPTS must be >= 1"},
		"bad duration":        {"LLM_TIMEOUT", "soon", "LLM_TIMEOUT"},
		"negative duration":   {"LLM_TIMEOUT", "-1s", "LLM_TIMEOUT must be > 0"},
		"zero rps":            {"RATE_LIMIT_RPS", "0", "RATE_LIMIT_RPS must be > 0"},
		"relative url":        {"WHATSAPP_API_URL", "/messages", "WHATSAPP_API_URL"},
		"empty addr":          {"ADDR", "", "ADDR must not be empty"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			_, err := ConfigFromEnv()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestConfigFromEnvReportsAllErrors(t *testing.T) {
	t.Setenv("WORKERS", "0")
	t.Setenv("QUEUE_SIZE", "-1")
	_, err := ConfigFromEnv()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"WORKERS", "QUEUE_SIZE"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %q, missing %q", err, want)
		}
	}
}
