package mail

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Hulul-Group/Tusk/internal/config"
)

type capture struct{ msgs []Message }

func (c *capture) Send(_ context.Context, m Message) error { c.msgs = append(c.msgs, m); return nil }

func cfg() *config.Config {
	return &config.Config{DefaultDomain: "kutt.test", SiteName: "Tusk", MailUser: "bot@kutt.test", MailPort: 587, ReportEmail: "abuse@kutt.test"}
}

func TestDisabled(t *testing.T) {
	m, err := New(cfg(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled() || !errors.Is(m.SendVerification(t.Context(), "a@b.co", "x"), ErrDisabled) {
		t.Fatal("disabled mailer must refuse")
	}
}

func TestTemplates(t *testing.T) {
	c := &capture{}
	m := NewWithSender(cfg(), c)
	ctx := t.Context()
	_ = m.SendVerification(ctx, "u@x.co", "tok-v")
	_ = m.SendChangeEmail(ctx, "new@x.co", "tok-c")
	_ = m.SendResetPassword(ctx, "u@x.co", "tok-r")
	_ = m.SendReport(ctx, "kutt.test/<script>")
	if len(c.msgs) != 4 {
		t.Fatal(len(c.msgs))
	}
	checks := []struct {
		subject, to, link string
	}{
		{"Verify your account", "u@x.co", "https://kutt.test/verify/tok-v"},
		{"Verify your new email address", "new@x.co", "https://kutt.test/verify-email/tok-c"},
		{"Reset your password", "u@x.co", "https://kutt.test/reset-password/tok-r"},
	}
	for i, ck := range checks {
		g := c.msgs[i]
		if g.Subject != ck.subject || g.To != ck.to || g.From != "bot@kutt.test" {
			t.Errorf("%d: %+v", i, g)
		}
		if !strings.Contains(g.Text, ck.link) || !strings.Contains(g.HTML, ck.link) {
			t.Errorf("%d: link %s missing", i, ck.link)
		}
		if strings.Contains(g.HTML, "{{") || strings.Contains(g.Text, "{{") {
			t.Errorf("%d: unreplaced placeholder", i)
		}
		if !strings.Contains(g.HTML, "Tusk") && i != 2 {
			t.Errorf("%d: site name missing", i)
		}
	}
	if !strings.HasPrefix(c.msgs[0].Text, "Thanks for creating an account on Tusk.") {
		t.Errorf("verify text: %q", c.msgs[0].Text)
	}
	r := c.msgs[3]
	if r.To != "abuse@kutt.test" || r.Subject != "[REPORT]" || r.Text != "kutt.test/<script>" || r.HTML != "kutt.test/&lt;script&gt;" {
		t.Errorf("report %+v", r)
	}
}
