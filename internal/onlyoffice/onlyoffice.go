// Package onlyoffice is the little of ONLYOFFICE Docs' contract docfind needs
// to put a real Word editor around a document in its archive (5.55.0): the
// HS256 tokens both sides sign with one shared secret, the editor's
// configuration, the callback the document server posts when a document has
// been saved, and the key that names one version of one file.
//
// The document server never reads the archive itself. docfind hands it a link
// to fetch the file from, and it hands back a link to fetch the edited file
// from; the secret is what stops anybody else doing either.
package onlyoffice

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

var b64 = base64.RawURLEncoding

// Sign makes an HS256 JSON Web Token of claims.
func Sign(secret []byte, claims any) (string, error) {
	if len(secret) == 0 {
		return "", errors.New("onlyoffice: no secret")
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	head := b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + b64.EncodeToString(body)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(head))
	return head + "." + b64.EncodeToString(m.Sum(nil)), nil
}

// ErrToken is every way a token can fail; the reason is not told apart for
// the caller on purpose.
var ErrToken = errors.New("onlyoffice: bad token")

// Verify checks an HS256 token and returns its claims. A token naming any
// other algorithm is refused, "none" included, and so is one whose "exp" has
// passed or whose "nbf" has not come.
func Verify(secret []byte, token string, now time.Time) (map[string]json.RawMessage, error) {
	parts := strings.Split(token, ".")
	if len(secret) == 0 || len(parts) != 3 {
		return nil, ErrToken
	}
	hb, err := b64.DecodeString(parts[0])
	if err != nil {
		return nil, ErrToken
	}
	var head struct {
		Alg string `json:"alg"`
	}
	if json.Unmarshal(hb, &head) != nil || head.Alg != "HS256" {
		return nil, ErrToken
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return nil, ErrToken
	}
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, m.Sum(nil)) {
		return nil, ErrToken
	}
	body, err := b64.DecodeString(parts[1])
	if err != nil {
		return nil, ErrToken
	}
	claims := map[string]json.RawMessage{}
	if json.Unmarshal(body, &claims) != nil {
		return nil, ErrToken
	}
	var t int64
	if raw, ok := claims["exp"]; ok && (json.Unmarshal(raw, &t) != nil || now.Unix() >= t) {
		return nil, ErrToken
	}
	if raw, ok := claims["nbf"]; ok && (json.Unmarshal(raw, &t) != nil || now.Unix() < t) {
		return nil, ErrToken
	}
	return claims, nil
}

// Key names one version of one file for the document server, which keeps
// its cache and its co-editing session by it: the same key is the same
// document to everybody who opens it, and a file changed on disk must get a
// new one or the editor shows the old text. At most 128 characters of
// [0-9A-Za-z_-].
func Key(id string, size int64, mod time.Time) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%d", id, size, mod.UnixNano()))
	return b64.EncodeToString(sum[:18])
}

// Kind is the editor a file opens in -- "word", "cell" or "slide" -- and
// whether docfind lets it be edited there rather than only read. Files the
// document server would have to convert (.doc, .xls, .rtf) open to be read:
// saving them would write a different format under the old name.
func Kind(name string) (documentType string, editable bool) {
	switch strings.ToLower(strings.TrimPrefix(path.Ext(name), ".")) {
	case "docx":
		return "word", true
	case "doc", "odt", "rtf", "txt", "pdf":
		return "word", false
	case "xlsx":
		return "cell", true
	case "xls", "ods", "csv":
		return "cell", false
	case "pptx":
		return "slide", true
	case "ppt", "odp":
		return "slide", false
	}
	return "", false
}

// The editor's configuration, as much of it as docfind sets.
type (
	Config struct {
		Document     Document     `json:"document"`
		DocumentType string       `json:"documentType"`
		EditorConfig EditorConfig `json:"editorConfig"`
		Type         string       `json:"type,omitempty"` // "desktop" or "mobile"
		Width        string       `json:"width,omitempty"`
		Height       string       `json:"height,omitempty"`
		Token        string       `json:"token,omitempty"`
	}
	Document struct {
		FileType    string      `json:"fileType"`
		Key         string      `json:"key"`
		Title       string      `json:"title"`
		URL         string      `json:"url"`
		Permissions Permissions `json:"permissions"`
	}
	Permissions struct {
		Edit     bool `json:"edit"`
		Download bool `json:"download"`
		Print    bool `json:"print"`
		Comment  bool `json:"comment"`
		Review   bool `json:"review"`
	}
	EditorConfig struct {
		CallbackURL   string        `json:"callbackUrl,omitempty"`
		Lang          string        `json:"lang"`
		Region        string        `json:"region,omitempty"`
		Mode          string        `json:"mode"` // "edit" or "view"
		User          User          `json:"user"`
		CoEditing     *CoEditing    `json:"coEditing,omitempty"`
		Customization Customization `json:"customization"`
	}
	User struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	CoEditing struct {
		Mode   string `json:"mode"` // "fast" or "strict"
		Change bool   `json:"change"`
	}
	Customization struct {
		Forcesave     bool   `json:"forcesave"`
		Autosave      bool   `json:"autosave"`
		CompactHeader bool   `json:"compactHeader,omitempty"`
		HideRulers    bool   `json:"hideRulers,omitempty"`
		Help          bool   `json:"help"`
		Feedback      bool   `json:"feedback"`
		Plugins       bool   `json:"plugins"`
		UITheme       string `json:"uiTheme,omitempty"`
	}
)

// Signed returns c with its token: the whole configuration, signed, which
// the document server checks before it opens anything.
func (c Config) Signed(secret []byte) (Config, error) {
	c.Token = ""
	tok, err := Sign(secret, c)
	if err != nil {
		return c, err
	}
	c.Token = tok
	return c, nil
}

// Callback is what the document server posts to callbackUrl.
type Callback struct {
	Key           string   `json:"key"`
	Status        int      `json:"status"`
	URL           string   `json:"url,omitempty"`
	FileType      string   `json:"filetype,omitempty"`
	Users         []string `json:"users,omitempty"`
	ForceSaveType int      `json:"forcesavetype,omitempty"`
	Actions       []struct {
		Type   int    `json:"type"`
		UserID string `json:"userid"`
	} `json:"actions,omitempty"`
	Token string `json:"token,omitempty"`
}

// The statuses a callback brings.
const (
	StatusMustSave   = 2 // everybody closed it and it changed: save url
	StatusSaveError  = 3
	StatusForceSaved = 6 // the Save button, while still open: save url
	StatusForceError = 7
)

// ReadCallback checks a callback's signature and returns what it says. The
// token comes in the body ("token", whose claims are the callback itself) or
// in the Authorization header ("Bearer <token>", whose claims carry it under
// "payload"), depending on the server's settings; the signed copy is what is
// believed, never the unsigned fields beside it.
func ReadCallback(secret []byte, body []byte, authorization string, now time.Time) (Callback, error) {
	var cb Callback
	if err := json.Unmarshal(body, &cb); err != nil {
		return Callback{}, err
	}
	tok := cb.Token
	wrapped := false
	if tok == "" {
		tok, _ = strings.CutPrefix(authorization, "Bearer ")
		wrapped = true
	}
	claims, err := Verify(secret, strings.TrimSpace(tok), now)
	if err != nil {
		return Callback{}, err
	}
	raw, err := json.Marshal(claims)
	if err != nil {
		return Callback{}, ErrToken
	}
	if p, ok := claims["payload"]; ok && wrapped {
		raw = p
	}
	var signed Callback
	if err := json.Unmarshal(raw, &signed); err != nil || signed.Key == "" {
		return Callback{}, ErrToken
	}
	signed.Token = ""
	return signed, nil
}
