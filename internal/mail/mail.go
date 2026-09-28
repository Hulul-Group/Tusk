// Package mail ports Kutt's server/mail (nodemailer) to github.com/wneessen/go-mail.
// The three HTML templates are Kutt's files verbatim (templates/); the
// placeholders {{domain}}, {{site_name}}, {{verification}}, {{resetpassword}}
// are substituted exactly like mail.js (string replace, no escaping, since
// values are config strings and server-generated UUID tokens).
package mail

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	gomail "github.com/wneessen/go-mail"

	"github.com/Hulul-Group/Tusk/internal/config"
)

//go:embed templates/*.html
var fs embed.FS

// Kutt text bodies (mail/text.js). NOTE: Kutt ships verifyMailText and
// changeEmailText with their first sentences swapped (text.js:1-11); Tusk
// uses the sentence that matches each mail's purpose.
const (
	verifyText = "Thanks for creating an account on {{site_name}}.\n\n" +
		"Please verify your email address using the link below.\n\n" +
		"https://{{domain}}/verify/{{verification}}"
	changeEmailText = "You're attempting to change your email address on {{site_name}}.\n\n" +
		"Please verify your email address using the link below.\n\n" +
		"https://{{domain}}/verify-email/{{verification}}"
	resetText = "A password reset has been requested for your account.\n\n" +
		"Please click on the button below to reset your password. There's no need to take any action if you didn't request this.\n\n" +
		"https://{{domain}}/reset-password/{{resetpassword}}"
)

// ErrDisabled is returned by every send when MAIL_ENABLED=false.
var ErrDisabled = errors.New("mail is not enabled")

// Message is an outgoing mail (exposed for tests via a custom Sender).
type Message struct {
	From, To, Subject, Text, HTML string
}

// Sender delivers a Message. The default is SMTP via go-mail.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Mailer sends Kutt's transactional mails.
type Mailer struct {
	enabled     bool
	from        string
	domain      string
	siteName    string
	reportEmail string
	sender      Sender
	verifyHTML  string
	changeHTML  string
	resetHTML   string
}

// New builds a Mailer from config. When MAIL_ENABLED=false it returns a
// disabled mailer whose sends return ErrDisabled.
func New(cfg *config.Config, log *slog.Logger) (*Mailer, error) {
	m := &Mailer{
		enabled:     cfg.MailEnabled,
		from:        cfg.MailFrom,
		domain:      cfg.DefaultDomain,
		siteName:    cfg.SiteName,
		reportEmail: cfg.ReportEmail,
	}
	if m.from == "" {
		m.from = cfg.MailUser // env.MAIL_FROM || env.MAIL_USER
	}
	if !m.enabled {
		return m, nil
	}
	load := func(name string) string {
		b, _ := fs.ReadFile("templates/" + name)
		return m.fill(string(b))
	}
	m.verifyHTML = load("template-verify.html")
	m.changeHTML = load("template-change-email.html")
	m.resetHTML = load("template-reset.html")
	s, err := newSMTP(cfg)
	if err != nil {
		return nil, err
	}
	m.sender = s
	return m, nil
}

// NewWithSender is New with an injected transport (tests).
func NewWithSender(cfg *config.Config, s Sender) *Mailer {
	c := *cfg
	c.MailEnabled = true
	c.MailHost = "unused"
	m, _ := New(&c, nil)
	m.sender = s
	return m
}

func (m *Mailer) fill(s string) string {
	return strings.NewReplacer("{{domain}}", m.domain, "{{site_name}}", m.siteName).Replace(s)
}

// Enabled reports MAIL_ENABLED.
func (m *Mailer) Enabled() bool { return m != nil && m.enabled }

func (m *Mailer) send(ctx context.Context, msg Message) error {
	if !m.Enabled() {
		return ErrDisabled
	}
	msg.From = m.from
	return m.sender.Send(ctx, msg)
}

// SendVerification ports mail.verification.
func (m *Mailer) SendVerification(ctx context.Context, to, token string) error {
	r := strings.NewReplacer("{{verification}}", token)
	return m.send(ctx, Message{To: to, Subject: "Verify your account",
		Text: r.Replace(m.fill(verifyText)), HTML: r.Replace(m.verifyHTML)})
}

// SendChangeEmail ports mail.changeEmail (to = the NEW address).
func (m *Mailer) SendChangeEmail(ctx context.Context, to, token string) error {
	r := strings.NewReplacer("{{verification}}", token)
	return m.send(ctx, Message{To: to, Subject: "Verify your new email address",
		Text: r.Replace(m.fill(changeEmailText)), HTML: r.Replace(m.changeHTML)})
}

// SendResetPassword ports mail.resetPasswordToken.
func (m *Mailer) SendResetPassword(ctx context.Context, to, token string) error {
	r := strings.NewReplacer("{{resetpassword}}", token)
	return m.send(ctx, Message{To: to, Subject: "Reset your password",
		Text: r.Replace(m.fill(resetText)), HTML: r.Replace(m.resetHTML)})
}

// SendReport ports mail.sendReportEmail. Kutt puts the raw user-supplied link
// into the HTML part; Tusk HTML-escapes it (the text part is unchanged).
func (m *Mailer) SendReport(ctx context.Context, link string) error {
	return m.send(ctx, Message{To: m.reportEmail, Subject: "[REPORT]", Text: link, HTML: html.EscapeString(link)})
}

// ---------- SMTP transport ----------

type smtpSender struct {
	c *gomail.Client
}

func newSMTP(cfg *config.Config) (*smtpSender, error) {
	opts := []gomail.Option{
		gomail.WithPort(cfg.MailPort),
		gomail.WithTimeout(15 * time.Second),
	}
	if cfg.MailSecure {
		// nodemailer secure:true = implicit TLS (SMTPS).
		opts = append(opts, gomail.WithSSLPort(false))
		opts = append(opts, gomail.WithPort(cfg.MailPort))
	} else {
		// nodemailer secure:false upgrades with STARTTLS when offered.
		opts = append(opts, gomail.WithTLSPortPolicy(gomail.TLSOpportunistic))
		opts = append(opts, gomail.WithPort(cfg.MailPort))
	}
	if cfg.MailUser != "" {
		opts = append(opts, gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover),
			gomail.WithUsername(cfg.MailUser), gomail.WithPassword(cfg.MailPassword))
	}
	c, err := gomail.NewClient(cfg.MailHost, opts...)
	if err != nil {
		return nil, fmt.Errorf("mail: %w", err)
	}
	return &smtpSender{c: c}, nil
}

func (s *smtpSender) Send(ctx context.Context, m Message) error {
	msg := gomail.NewMsg()
	if err := msg.From(m.From); err != nil {
		return err
	}
	if err := msg.To(m.To); err != nil {
		return err
	}
	msg.Subject(m.Subject)
	msg.SetBodyString(gomail.TypeTextPlain, m.Text)
	msg.AddAlternativeString(gomail.TypeTextHTML, m.HTML)
	return s.c.DialAndSendWithContext(ctx, msg)
}
