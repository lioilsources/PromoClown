// Package telegram is the handful of Bot API calls the approval bot needs.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	Token   string
	BaseURL string
	HTTP    *http.Client
}

func New(token string) *Client {
	return &Client{Token: token, BaseURL: "https://api.telegram.org", HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Text      string `json:"text"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
	URL          string `json:"url,omitempty"`
}

type Keyboard struct {
	InlineKeyboard [][]Button `json:"inline_keyboard"`
}

// Error is a Bot API refusal.
type Error struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *Error) Error() string { return fmt.Sprintf("telegram %d: %s", e.Code, e.Description) }

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (c *Client) endpoint(method string) string {
	return c.BaseURL + "/bot" + c.Token + "/" + method
}

func (c *Client) send(req *http.Request, out any) error {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The URL carries the token; never let it reach a log line.
		return fmt.Errorf("telegram %s: %s", pathMethod(req.URL), strings.ReplaceAll(err.Error(), c.Token, "<token>"))
	}
	defer resp.Body.Close()
	var env envelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("telegram %s: HTTP %d: %w", pathMethod(req.URL), resp.StatusCode, err)
	}
	if !env.OK {
		return &Error{Code: env.ErrorCode, Description: env.Description, RetryAfter: env.Parameters.RetryAfter}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Result, out)
}

func pathMethod(u *url.URL) string {
	return u.Path[strings.LastIndex(u.Path, "/")+1:]
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	buf, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.send(req, out)
}

// GetMe checks the token and returns the bot's own user.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var out User
	return out, c.call(ctx, "getMe", map[string]any{}, &out)
}

// GetUpdates long-polls. Only one process may poll a bot token at a time;
// a second one gets 409 Conflict.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSec int) ([]Update, error) {
	var out []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	}, &out)
	return out, err
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, html string, kb *Keyboard) (Message, error) {
	params := map[string]any{
		"chat_id":              chatID,
		"text":                 html,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
	}
	if kb != nil {
		params["reply_markup"] = kb
	}
	var out Message
	return out, c.call(ctx, "sendMessage", params, &out)
}

// SendMedia uploads a photo or video with an HTML caption (max 1024 chars).
func (c *Client) SendMedia(ctx context.Context, chatID int64, kind, filename string, r io.Reader, caption string, kb *Keyboard) (Message, error) {
	method, field := "sendPhoto", "photo"
	if kind == "video" {
		method, field = "sendVideo", "video"
	}
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := mw.WriteField("chat_id", strconv.FormatInt(chatID, 10))
		if err == nil && caption != "" {
			if err = mw.WriteField("caption", caption); err == nil {
				err = mw.WriteField("parse_mode", "HTML")
			}
		}
		if err == nil && kb != nil {
			var buf []byte
			if buf, err = json.Marshal(kb); err == nil {
				err = mw.WriteField("reply_markup", string(buf))
			}
		}
		if err == nil {
			var part io.Writer
			if part, err = mw.CreateFormFile(field, filename); err == nil {
				_, err = io.Copy(part, r)
			}
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), pr)
	if err != nil {
		return Message{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var out Message
	return out, c.send(req, &out)
}

func (c *Client) AnswerCallback(ctx context.Context, callbackID, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID, "text": text}, nil)
}

// ClearKeyboard removes the inline buttons from a message. "Not modified"
// counts as success.
func (c *Client) ClearKeyboard(ctx context.Context, chatID, messageID int64) error {
	err := c.call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id": chatID, "message_id": messageID, "reply_markup": Keyboard{InlineKeyboard: [][]Button{}},
	}, nil)
	if e, ok := err.(*Error); ok && strings.Contains(e.Description, "not modified") {
		return nil
	}
	return err
}
