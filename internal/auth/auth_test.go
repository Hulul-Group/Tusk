package auth

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

type nodeVectors struct {
	Bcrypt struct {
		Password string `json:"password"`
		Hash     string `json:"hash"`
	} `json:"bcrypt"`
	JWT struct {
		Secret  string `json:"secret"`
		Token   string `json:"token"`
		Expired string `json:"expired"`
		StrSub  string `json:"strSub"`
	} `json:"jwt"`
}

// testdata/node_vectors.json was produced by running Kutt's own dependencies
// (bcryptjs 2.4.3, jsonwebtoken 9 from /root/Omran/kutt/package-lock.json)
// in a scratch copy: node vectors.js > testdata/node_vectors.json.
func loadVectors(t *testing.T) nodeVectors {
	t.Helper()
	b, err := os.ReadFile("testdata/node_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v nodeVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestBcryptjsHashVerifies(t *testing.T) {
	v := loadVectors(t)
	if !strings.HasPrefix(v.Bcrypt.Hash, "$2a$12$") {
		t.Fatalf("expected bcryptjs $2a$12$ hash, got %q", v.Bcrypt.Hash)
	}
	if !ComparePassword(v.Bcrypt.Hash, v.Bcrypt.Password) {
		t.Fatal("bcryptjs hash did not verify in Go")
	}
	if ComparePassword(v.Bcrypt.Hash, v.Bcrypt.Password+"x") {
		t.Fatal("wrong password verified")
	}
}

func TestHashPasswordCost12(t *testing.T) {
	h, err := HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$2a$12$") {
		t.Fatalf("want $2a$12$ prefix, got %q", h)
	}
	if !ComparePassword(h, "hunter22") || ComparePassword(h, "hunter23") {
		t.Fatal("roundtrip failed")
	}
	long := strings.Repeat("a", 100)
	h2, err := HashPassword(long)
	if err != nil || !ComparePassword(h2, long[:72]+"zzz") {
		t.Fatal("72-byte truncation must match bcryptjs")
	}
}

func TestNodeJWTValidatesInGo(t *testing.T) {
	v := loadVectors(t)
	tk := NewTokens(v.JWT.Secret)
	c, err := tk.Verify(v.JWT.Token)
	if err != nil {
		t.Fatalf("node token rejected: %v", err)
	}
	if c.UserID != 42 {
		t.Fatalf("sub=%d", c.UserID)
	}
	if _, err := tk.Verify(v.JWT.Expired); err == nil {
		t.Fatal("expired node token accepted")
	}
	if _, err := tk.Verify(v.JWT.StrSub); err == nil {
		t.Fatal("string-sub token must be rejected (passport.js:21)")
	}
	if _, err := NewTokens("other").Verify(v.JWT.Token); err == nil {
		t.Fatal("wrong secret accepted")
	}
}

func TestGoJWTShape(t *testing.T) {
	tk := NewTokens("s")
	now := time.Unix(1_700_000_000, 0)
	tk.SetClock(func() time.Time { return now })
	s, err := tk.Sign(7)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(s, ".")
	hdr, _ := base64.RawURLEncoding.DecodeString(parts[0])
	body, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if string(hdr) != `{"alg":"HS256","typ":"JWT"}` {
		t.Fatalf("header %s", hdr)
	}
	// Same key order and values as jsonwebtoken.sign({iss,sub,iat,exp}).
	if want := `{"iss":"ApiAuth","sub":7,"iat":1700000000,"exp":1700604800}`; string(body) != want {
		t.Fatalf("claims %s want %s", body, want)
	}
	c, err := tk.Verify(s)
	if err != nil || c.UserID != 7 {
		t.Fatalf("verify: %v %+v", err, c)
	}
}

func TestGenerateRandomPassword(t *testing.T) {
	for i := 0; i < 200; i++ {
		p := GenerateRandomPassword()
		if len(p) < 24 || len(p) > 64 {
			t.Fatalf("len %d", len(p))
		}
		for _, c := range []byte(p) {
			if c < 33 || c > 125 {
				t.Fatalf("char %d", c)
			}
		}
	}
}
