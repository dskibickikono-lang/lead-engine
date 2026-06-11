package deliver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
	"unicode/utf8"
)

const signalMaxPart = 4000

type SignalClient struct {
	APIURL     string
	Number     string
	Recipients []string
	HTTP       *http.Client
	Backoff    time.Duration // base backoff; default 2s
}

func (c *SignalClient) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

func (c *SignalClient) backoff() time.Duration {
	if c.Backoff > 0 {
		return c.Backoff
	}
	return 2 * time.Second
}

func splitMessage(msg string, max int) []string {
	if len(msg) <= max {
		return []string{msg}
	}
	var parts []string
	for len(msg) > 0 {
		n := max
		if n >= len(msg) {
			n = len(msg)
		} else {
			// Never split a multi-byte UTF-8 rune across parts.
			for n > 0 && !utf8.RuneStart(msg[n]) {
				n--
			}
			if n == 0 {
				n = max
			}
		}
		parts = append(parts, msg[:n])
		msg = msg[n:]
	}
	for i := range parts {
		parts[i] = fmt.Sprintf("(%d/%d)\n%s", i+1, len(parts), parts[i])
	}
	return parts
}

func (c *SignalClient) Send(ctx context.Context, msg string) error {
	for _, part := range splitMessage(msg, signalMaxPart) {
		body, _ := json.Marshal(map[string]any{
			"message":    part,
			"number":     c.Number,
			"recipients": c.Recipients,
		})
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(c.backoff() << attempt):
				}
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				c.APIURL+"/v2/send", bytes.NewReader(body))
			if err != nil {
				return err
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := c.http().Do(req)
			if err != nil {
				lastErr = err
				continue
			}
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				lastErr = nil
				break
			}
			lastErr = fmt.Errorf("signal: status %d", resp.StatusCode)
		}
		if lastErr != nil {
			return fmt.Errorf("signal send: %w", lastErr)
		}
	}
	return nil
}
