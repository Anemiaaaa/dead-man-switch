package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Telegram sends reminders through the Bot API.
//
// Written against net/http rather than a bot library: one POST with two fields
// does not justify a dependency, and this way the timeout and the error
// handling are visible.
type Telegram struct {
	Token  string
	ChatID string
	Client *http.Client
}

func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{
		Token:  token,
		ChatID: chatID,
		Client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *Telegram) Notify(ctx context.Context, r Reminder) error {
	body, err := json.Marshal(map[string]string{
		"chat_id": t.ChatID,
		"text":    r.Message(),
	})
	if err != nil {
		return fmt.Errorf("telegram: encoding message: %w", err)
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.Client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: sending: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The API explains itself in the body; the status alone is not enough
		// to tell a bad token from a bad chat id.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram: %s: %s", resp.Status, bytes.TrimSpace(detail))
	}

	return nil
}
