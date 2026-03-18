// Package alert — notifier implementations.
// The Notifier interface is intentionally minimal: one method, one error return.
// Add new destinations by implementing the interface.
package alert

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// Notifier sends a notification when an alert fires or resolves.
type Notifier interface {
	Notify(ev AlertEvent) error
}

// ── Multi-notifier ────────────────────────────────────────────────────────────

// MultiNotifier fans out to multiple Notifiers. All are tried even if one fails.
type MultiNotifier struct {
	notifiers []Notifier
}

// NewMultiNotifier creates a MultiNotifier from the provided list.
// Nil entries are skipped so callers can conditionally include notifiers.
func NewMultiNotifier(ns ...Notifier) *MultiNotifier {
	m := &MultiNotifier{}
	for _, n := range ns {
		if n != nil {
			m.notifiers = append(m.notifiers, n)
		}
	}
	return m
}

func (m *MultiNotifier) Notify(ev AlertEvent) error {
	var errs []string
	for _, n := range m.notifiers {
		if err := n.Notify(ev); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("notifier errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ── Log notifier (always active, zero config) ─────────────────────────────────

// LogNotifier prints alerts to stdout. Always enabled; useful for testing.
type LogNotifier struct{}

func (l *LogNotifier) Notify(ev AlertEvent) error {
	ts := time.Now().Format("2006-01-02 15:04:05")
	switch ev.State {
	case StateFiring:
		fmt.Printf("[%s] ALERT FIRING  name=%s severity=%s value=%.2f threshold=%s%.2f\n",
			ts, ev.Rule.Name, ev.Rule.Severity, ev.Value, ev.Rule.Op, ev.Rule.Threshold)
		if summary, ok := ev.Rule.Annotations["summary"]; ok {
			fmt.Printf("              summary: %s\n", summary)
		}
	case StateOK:
		fmt.Printf("[%s] ALERT RESOLVED name=%s value=%.2f\n", ts, ev.Rule.Name, ev.Value)
	}
	return nil
}

// ── Slack notifier ────────────────────────────────────────────────────────────

// SlackConfig holds configuration for Slack webhook notifications.
type SlackConfig struct {
	// WebhookURL is the Incoming Webhook URL from your Slack app.
	// e.g. https://hooks.slack.com/services/T.../B.../...
	WebhookURL string

	// Channel overrides the default channel set on the webhook (optional).
	Channel string

	// Username overrides the bot name shown in Slack (optional).
	Username string

	// Timeout for the HTTP POST. Defaults to 10s.
	Timeout time.Duration
}

// SlackNotifier sends alert notifications to a Slack channel via Incoming Webhooks.
type SlackNotifier struct {
	cfg    SlackConfig
	client *http.Client
}

// NewSlackNotifier creates a SlackNotifier. Returns nil if WebhookURL is empty.
func NewSlackNotifier(cfg SlackConfig) *SlackNotifier {
	if cfg.WebhookURL == "" {
		return nil
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &SlackNotifier{
		cfg:    cfg,
		client: &http.Client{Timeout: timeout},
	}
}

type slackPayload struct {
	Text        string            `json:"text,omitempty"`
	Username    string            `json:"username,omitempty"`
	Channel     string            `json:"channel,omitempty"`
	Attachments []slackAttachment `json:"attachments,omitempty"`
}

type slackAttachment struct {
	Color  string       `json:"color"`
	Title  string       `json:"title"`
	Text   string       `json:"text"`
	Fields []slackField `json:"fields"`
	Footer string       `json:"footer"`
	Ts     int64        `json:"ts"`
}

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

func (s *SlackNotifier) Notify(ev AlertEvent) error {
	color, emoji, statusText := slackStyle(ev)
	summary := ev.Rule.Annotations["summary"]
	if summary == "" {
		summary = fmt.Sprintf("%s %s %.2f (value=%.2f)", ev.Rule.Metric, ev.Rule.Op, ev.Rule.Threshold, ev.Value)
	}

	att := slackAttachment{
		Color:  color,
		Title:  fmt.Sprintf("%s %s", emoji, ev.Rule.Name),
		Text:   summary,
		Footer: "rhelmon alert engine",
		Ts:     time.Now().Unix(),
		Fields: []slackField{
			{Title: "Metric", Value: ev.Rule.Metric, Short: true},
			{Title: "Value", Value: fmt.Sprintf("%.2f", ev.Value), Short: true},
			{Title: "Threshold", Value: fmt.Sprintf("%s %.2f", ev.Rule.Op, ev.Rule.Threshold), Short: true},
			{Title: "Severity", Value: ev.Rule.Severity, Short: true},
			{Title: "State", Value: statusText, Short: true},
		},
	}

	payload := slackPayload{
		Username:    s.cfg.Username,
		Channel:     s.cfg.Channel,
		Attachments: []slackAttachment{att},
	}
	if payload.Username == "" {
		payload.Username = "rhelmon"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("slack: marshal: %w", err)
	}

	resp, err := s.client.Post(s.cfg.WebhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack: post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func slackStyle(ev AlertEvent) (color, emoji, statusText string) {
	if ev.State == StateOK {
		return "#2eb886", "✅", "resolved"
	}
	switch ev.Rule.Severity {
	case "critical":
		return "#cc0000", "🚨", "firing"
	default:
		return "#ff9800", "⚠️", "firing"
	}
}

// ── Email notifier (SMTP) ─────────────────────────────────────────────────────

// EmailConfig holds SMTP configuration.
type EmailConfig struct {
	// SMTPHost e.g. "smtp.gmail.com"
	SMTPHost string
	// SMTPPort e.g. 587
	SMTPPort int
	// Username for SMTP auth
	Username string
	// Password for SMTP auth
	Password string
	// From address e.g. "rhelmon@example.com"
	From string
	// To is a list of recipient addresses
	To []string
}

// EmailNotifier sends alert notifications via SMTP.
type EmailNotifier struct {
	cfg EmailConfig
}

// NewEmailNotifier creates an EmailNotifier. Returns nil if SMTPHost is empty.
func NewEmailNotifier(cfg EmailConfig) *EmailNotifier {
	if cfg.SMTPHost == "" || len(cfg.To) == 0 {
		return nil
	}
	return &EmailNotifier{cfg: cfg}
}

func (e *EmailNotifier) Notify(ev AlertEvent) error {
	subject, body := emailContent(ev)
	addr := fmt.Sprintf("%s:%d", e.cfg.SMTPHost, e.cfg.SMTPPort)

	var auth smtp.Auth
	if e.cfg.Username != "" {
		auth = smtp.PlainAuth("", e.cfg.Username, e.cfg.Password, e.cfg.SMTPHost)
	}

	msg := fmt.Sprintf(
		"From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s",
		e.cfg.From,
		strings.Join(e.cfg.To, ", "),
		subject,
		body,
	)

	if err := smtp.SendMail(addr, auth, e.cfg.From, e.cfg.To, []byte(msg)); err != nil {
		return fmt.Errorf("email: sendmail: %w", err)
	}
	return nil
}

func emailContent(ev AlertEvent) (subject, body string) {
	ts := time.Now().Format("2006-01-02 15:04:05 MST")
	summary := ev.Rule.Annotations["summary"]
	if summary == "" {
		summary = fmt.Sprintf("%s %s %.2f", ev.Rule.Metric, ev.Rule.Op, ev.Rule.Threshold)
	}

	if ev.State == StateOK {
		subject = fmt.Sprintf("[RESOLVED] %s", ev.Rule.Name)
		body = fmt.Sprintf(
			"Alert resolved at %s\n\nName:     %s\nMetric:   %s\nValue:    %.2f\nSummary:  %s\n",
			ts, ev.Rule.Name, ev.Rule.Metric, ev.Value, summary,
		)
	} else {
		subject = fmt.Sprintf("[%s] %s", strings.ToUpper(ev.Rule.Severity), ev.Rule.Name)
		body = fmt.Sprintf(
			"Alert firing at %s\n\nName:      %s\nSeverity:  %s\nMetric:    %s\nValue:     %.2f\nThreshold: %s %.2f\nSummary:   %s\n",
			ts, ev.Rule.Name, ev.Rule.Severity, ev.Rule.Metric,
			ev.Value, ev.Rule.Op, ev.Rule.Threshold, summary,
		)
	}
	return
}
