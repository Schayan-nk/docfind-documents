package onlyoffice

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var secret = []byte("a shared secret of some length")

func TestATokenSignedIsATokenVerified(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tok, err := Sign(secret, map[string]any{"id": "296/148/Inquiry/x.docx", "exp": now.Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	c, err := Verify(secret, tok, now)
	if err != nil || string(c["id"]) != `"296/148/Inquiry/x.docx"` {
		t.Fatalf("%v %s", err, c["id"])
	}
	if _, err := Verify(secret, tok, now.Add(2*time.Minute)); err == nil {
		t.Error("an expired token was believed")
	}
	if _, err := Verify([]byte("another secret"), tok, now); err == nil {
		t.Error("a token signed with another secret was believed")
	}
	parts := strings.Split(tok, ".")
	body, _ := base64.RawURLEncoding.DecodeString(parts[1])
	forged := strings.Replace(string(body), "296", "999", 1)
	if _, err := Verify(secret, parts[0]+"."+base64.RawURLEncoding.EncodeToString([]byte(forged))+"."+parts[2], now); err == nil {
		t.Error("a changed claim was believed")
	}
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + parts[1] + "."
	if _, err := Verify(secret, none, now); err == nil {
		t.Error(`an "alg":"none" token was believed`)
	}
	if _, err := Verify(nil, tok, now); err == nil {
		t.Error("a token was believed with no secret")
	}
}

// The callback's token arrives in the body or, wrapped in "payload", in the
// Authorization header; what is believed is the signed copy, not the fields
// beside it.
func TestACallbackIsBelievedOnlyAsSigned(t *testing.T) {
	now := time.Now()
	signed := Callback{Key: "k1", Status: StatusMustSave, URL: "http://ds/cache/files/k1/output.docx", Users: []string{"t.amini"}}
	tok, _ := Sign(secret, signed)
	body, _ := json.Marshal(map[string]any{"key": "k1", "status": 2, "url": "http://evil.example/x.docx", "token": tok})
	cb, err := ReadCallback(secret, body, "", now)
	if err != nil || cb.URL != signed.URL || cb.Users[0] != "t.amini" {
		t.Fatalf("body token: %v %+v", err, cb)
	}
	wrapped, _ := Sign(secret, map[string]any{"payload": signed})
	plain, _ := json.Marshal(signed)
	if cb, err := ReadCallback(secret, plain, "Bearer "+wrapped, now); err != nil || cb.Key != "k1" || cb.Status != StatusMustSave {
		t.Fatalf("header token: %v %+v", err, cb)
	}
	if _, err := ReadCallback(secret, plain, "", now); err == nil {
		t.Error("an unsigned callback was believed")
	}
	other, _ := Sign([]byte("not ours"), signed)
	if _, err := ReadCallback(secret, plain, "Bearer "+other, now); err == nil {
		t.Error("a callback signed by somebody else was believed")
	}
}

func TestAKeyChangesWithTheFile(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	a := Key("1/2/x.docx", 100, at)
	if a != Key("1/2/x.docx", 100, at) {
		t.Error("the same file got two keys")
	}
	for _, b := range []string{Key("1/2/x.docx", 101, at), Key("1/2/x.docx", 100, at.Add(time.Second)), Key("1/2/y.docx", 100, at)} {
		if a == b {
			t.Error("a changed file kept its key")
		}
	}
	if len(a) > 128 || strings.ContainsAny(a, "+/=.") {
		t.Errorf("a key the document server would refuse: %q", a)
	}
}

func TestOnlyTheFormatsSavedAsThemselvesAreEditable(t *testing.T) {
	for name, want := range map[string]struct {
		kind string
		edit bool
	}{"a.DOCX": {"word", true}, "a.doc": {"word", false}, "b.xlsx": {"cell", true}, "c.pptx": {"slide", true}, "d.pdf": {"word", false}, "e.zip": {"", false}} {
		if k, e := Kind(name); k != want.kind || e != want.edit {
			t.Errorf("%s: %s %v", name, k, e)
		}
	}
}

func TestTheConfigIsSignedWhole(t *testing.T) {
	c := Config{DocumentType: "word", Document: Document{FileType: "docx", Key: "k", Title: "x.docx", URL: "http://docfind/x"},
		EditorConfig: EditorConfig{Mode: "edit", Lang: "fa", User: User{ID: "t.amini", Name: "تینا امینی"}}}
	s, err := c.Signed(secret)
	if err != nil || s.Token == "" {
		t.Fatal(err)
	}
	claims, err := Verify(secret, s.Token, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := claims["token"]; ok {
		t.Error("the token signs itself")
	}
	var doc Document
	json.Unmarshal(claims["document"], &doc)
	if doc.URL != "http://docfind/x" || doc.Key != "k" {
		t.Errorf("the signed document: %+v", doc)
	}
}
