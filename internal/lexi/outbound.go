package lexi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"golang.org/x/time/rate"
)

const maxDrainBytes = 4 << 10

type OutboundMessage struct {
	MessageID string
	To        string
	Text      string
}

type outboundText struct {
	Body string `json:"body"`
}

type outboundPayload struct {
	MessagingProduct string       `json:"messaging_product"`
	To               string       `json:"to"`
	Type             string       `json:"type"`
	Text             outboundText `json:"text"`
}

type WhatsAppSender struct {
	url     string
	token   string
	client  *http.Client
	policy  retryPolicy
	timeout time.Duration
	log     *slog.Logger
	limiter *rate.Limiter
}

func NewWhatsAppSender(cfg Config, client *http.Client, log *slog.Logger) *WhatsAppSender {
	if client == nil {
		client = &http.Client{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &WhatsAppSender{
		url:     cfg.WhatsAppURL,
		token:   cfg.WhatsAppToken,
		client:  client,
		policy:  cfg.retryPolicy(),
		timeout: cfg.OutboundTimeout,
		log:     log,
		limiter: rate.NewLimiter(rate.Inf, 0),
	}
}

func (s *WhatsAppSender) Send(ctx context.Context, msg OutboundMessage) (attempts int, err error) {
	payload := outboundPayload{
		MessagingProduct: "whatsapp",
		To:               msg.To,
		Type:             "text",
		Text:             outboundText{Body: msg.Text},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, permanent(err)
	}
	err = retry(ctx, s.policy, func(ctx context.Context) error {
		attempts++
		return s.attempt(ctx, msg, raw)
	})
	return attempts, err
}

func (s *WhatsAppSender) attempt(ctx context.Context, msg OutboundMessage, raw []byte) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := s.limiter.Wait(ctx); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(raw))
	if err != nil {
		return permanent(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.log.Warn("outbound attempt failed", "message_id", msg.MessageID, "err", err)
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()
	}()
	switch {
	case resp.StatusCode/100 == 2:
		var body struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxDrainBytes)).Decode(&body); err == nil && len(body.Messages) > 0 && body.Messages[0].ID != "" {
			s.log.Info("outbound sent", "message_id", msg.MessageID, "to", hashPhone(msg.To), "wamid", body.Messages[0].ID)
		} else {
			s.log.Info("outbound sent", "message_id", msg.MessageID, "to", hashPhone(msg.To))
		}
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError:
		err := fmt.Errorf("whatsapp: status %d", resp.StatusCode)
		s.log.Warn("outbound attempt failed", "message_id", msg.MessageID, "err", err)
		return err
	default:
		return permanent(fmt.Errorf("whatsapp: status %d", resp.StatusCode))
	}
}
