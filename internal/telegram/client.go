// Copyright 2026 The Kamerton Authors
// SPDX-License-Identifier: AGPL-3.0-only

// Package telegram sets up Telegram notifications from the UI; delivery stays with Alertmanager.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultAPIURL = "https://api.telegram.org"

// tokenPattern is the @BotFather token shape "<bot id>:<secret>", checked before the
// token leaves Kamerton.
var tokenPattern = regexp.MustCompile(`^([0-9]{5,20}):[A-Za-z0-9_-]{30,64}$`)

var ErrMalformedToken = errors.New("this does not look like a bot token: expected <digits>:<letters, digits, - and _> as issued by @BotFather")

func ValidTokenFormat(s string) bool { return tokenPattern.MatchString(s) }

func BotIDFromToken(token string) (int64, bool) {
	m := tokenPattern.FindStringSubmatch(token)
	if m == nil {
		return 0, false
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	return id, err == nil
}

type APIError struct {
	Code            int    `json:"code"`
	Description     string `json:"description"`
	MigrateToChatID int64  `json:"migrateToChatID,omitempty"`
	// RetryAfter is in seconds.
	RetryAfter int `json:"retryAfter,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram: %d %s", e.Code, e.Description)
}

func IsUnauthorized(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Code == http.StatusUnauthorized || ae.Code == http.StatusNotFound)
}

// IsConflict reports whether getUpdates is blocked by another consumer or a webhook.
func IsConflict(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == http.StatusConflict
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"` // private, group, supergroup, channel
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

func (c Chat) DisplayTitle() string {
	if c.Title != "" {
		return c.Title
	}
	name := strings.TrimSpace(c.FirstName + " " + c.LastName)
	if name == "" && c.Username != "" {
		name = "@" + c.Username
	}
	return name
}

type ChatMember struct {
	Status   string `json:"status"` // creator, administrator, member, restricted, left, kicked
	User     User   `json:"user"`
	IsMember *bool  `json:"is_member,omitempty"`
}

func (m ChatMember) Present() bool {
	switch m.Status {
	case "creator", "administrator", "member":
		return true
	case "restricted":
		return m.IsMember == nil || *m.IsMember
	default:
		return false
	}
}

type ChatMemberUpdated struct {
	Chat          Chat       `json:"chat"`
	From          User       `json:"from"`
	Date          int64      `json:"date"`
	OldChatMember ChatMember `json:"old_chat_member"`
	NewChatMember ChatMember `json:"new_chat_member"`
}

type Message struct {
	MessageID         int64  `json:"message_id"`
	Chat              Chat   `json:"chat"`
	Date              int64  `json:"date"`
	NewChatTitle      string `json:"new_chat_title,omitempty"`
	MigrateToChatID   int64  `json:"migrate_to_chat_id,omitempty"`
	MigrateFromChatID int64  `json:"migrate_from_chat_id,omitempty"`
	NewChatMembers    []User `json:"new_chat_members,omitempty"`
	LeftChatMember    *User  `json:"left_chat_member,omitempty"`
}

type Update struct {
	UpdateID          int64              `json:"update_id"`
	Message           *Message           `json:"message,omitempty"`
	ChannelPost       *Message           `json:"channel_post,omitempty"`
	MyChatMember      *ChatMemberUpdated `json:"my_chat_member,omitempty"`
	EditedMessage     *Message           `json:"edited_message,omitempty"`
	EditedChannelPost *Message           `json:"edited_channel_post,omitempty"`
}

// AllowedUpdates are requested explicitly because getUpdates remembers the
// last allowed_updates of the bot. message and channel_post carry migrations,
// title changes and chats in which someone talks to the bot.
var AllowedUpdates = []string{"my_chat_member", "message", "channel_post"}

type Client struct {
	// BaseURL defaults to DefaultAPIURL. Tests point it at a fake server.
	BaseURL string
	HTTP    *http.Client
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		MigrateToChatID int64 `json:"migrate_to_chat_id"`
		RetryAfter      int   `json:"retry_after"`
	} `json:"parameters"`
}

// call never returns an error that contains the token.
func (c *Client) call(ctx context.Context, token, method string, params any, out any) error {
	if !ValidTokenFormat(token) {
		return ErrMalformedToken
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultAPIURL
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(base, "/") + "/bot" + token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return scrub(fmt.Errorf("telegram %s: build request: %w", method, err), token)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			// url.Error prints the request URL, which contains the token.
			err = ue.Err
		}
		return scrub(fmt.Errorf("telegram %s: %w", method, err), token)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return scrub(fmt.Errorf("telegram %s: read response: %w", method, err), token)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("telegram %s: unexpected response (HTTP %d)", method, resp.StatusCode)
	}
	if !env.OK {
		ae := &APIError{Code: env.ErrorCode, Description: scrubString(env.Description, token)}
		if ae.Code == 0 {
			ae.Code = resp.StatusCode
		}
		if env.Parameters != nil {
			ae.MigrateToChatID = env.Parameters.MigrateToChatID
			ae.RetryAfter = env.Parameters.RetryAfter
		}
		return ae
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %v", method, err)
		}
	}
	return nil
}

func (c *Client) GetMe(ctx context.Context, token string) (*User, error) {
	var u User
	if err := c.call(ctx, token, "getMe", struct{}{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (c *Client) GetChat(ctx context.Context, token string, chatID int64) (*Chat, error) {
	var ch Chat
	if err := c.call(ctx, token, "getChat", map[string]any{"chat_id": chatID}, &ch); err != nil {
		return nil, err
	}
	return &ch, nil
}

func (c *Client) GetChatMember(ctx context.Context, token string, chatID, userID int64) (*ChatMember, error) {
	var m ChatMember
	if err := c.call(ctx, token, "getChatMember", map[string]any{"chat_id": chatID, "user_id": userID}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (c *Client) SendMessage(ctx context.Context, token string, chatID int64, html string) error {
	return c.call(ctx, token, "sendMessage", map[string]any{
		"chat_id": chatID, "text": html, "parse_mode": "HTML",
		"link_preview_options": map[string]any{"is_disabled": true},
	}, nil)
}

func (c *Client) GetUpdates(ctx context.Context, token string, offset int64, timeout time.Duration) ([]Update, error) {
	var ups []Update
	params := map[string]any{"timeout": int(timeout / time.Second), "allowed_updates": AllowedUpdates}
	if offset != 0 {
		params["offset"] = offset
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+15*time.Second)
	defer cancel()
	if err := c.call(ctx, token, "getUpdates", params, &ups); err != nil {
		return nil, err
	}
	return ups, nil
}

func scrub(err error, token string) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	if token != "" && strings.Contains(s, token) {
		return errors.New(scrubString(s, token))
	}
	if _, secret, ok := strings.Cut(token, ":"); ok && secret != "" && strings.Contains(s, secret) {
		return errors.New(scrubString(s, token))
	}
	return err
}

func scrubString(s, token string) string {
	if token == "" {
		return s
	}
	s = strings.ReplaceAll(s, token, "<token>")
	if _, secret, ok := strings.Cut(token, ":"); ok && secret != "" {
		s = strings.ReplaceAll(s, secret, "<token>")
	}
	return s
}
