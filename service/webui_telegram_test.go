package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Protomothis/smartthings-pc-control/service/notify"
	"github.com/Protomothis/smartthings-pc-control/service/secret"
	"github.com/Protomothis/smartthings-pc-control/service/telegram"
)

const testBotToken = "123456789:ABCdefGHIjklMNOpqrSTUvwxYZ0123456789"

// fakeTelegram is an httptest.Server standing in for api.telegram.org. It
// records every call (token from the path, method, decoded body) and answers
// per method from replies; unknown methods get a Bot API "not found".
type fakeTelegram struct {
	mu      sync.Mutex
	calls   []fakeCall
	replies map[string]string
}

type fakeCall struct {
	token  string
	method string
	body   map[string]any
}

// useFakeTelegram points the service at a fake Bot API for the test and
// restores the real endpoint afterwards.
func useFakeTelegram(t *testing.T) *fakeTelegram {
	t.Helper()
	f := &fakeTelegram{replies: map[string]string{
		"sendMessage": `{"ok":true,"result":{"message_id":7,"chat":{"id":42,"type":"private"}}}`,
		"getMe":       `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"PC","last_name":"Bot","username":"pcbot"}}`,
		"getUpdates": `{"ok":true,"result":[
			{"update_id":1,"message":{"message_id":1,"chat":{"id":42,"type":"private","first_name":"Kim","last_name":"Lump","username":"lump"},"text":"/start"}},
			{"update_id":2,"message":{"message_id":2,"chat":{"id":42,"type":"private","first_name":"Kim","last_name":"Lump","username":"lump"},"text":"hi"}},
			{"update_id":3,"message":{"message_id":3,"chat":{"id":-100,"type":"group","title":"Home"},"text":"/status"}},
			{"update_id":4,"callback_query":{"id":"cb","from":{"id":5,"first_name":"x"},"message":{"message_id":9,"chat":{"id":42,"type":"private","first_name":"Kim"}},"data":"cancel:"}}
		]}`,
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// /bot<token>/<method>
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
		call := fakeCall{token: parts[0], body: map[string]any{}}
		if len(parts) > 1 {
			call.method = parts[1]
		}
		raw, _ := io.ReadAll(r.Body)
		if len(raw) > 0 {
			json.Unmarshal(raw, &call.body)
		}
		f.mu.Lock()
		f.calls = append(f.calls, call)
		reply, ok := f.replies[call.method]
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
			return
		}
		fmt.Fprint(w, reply)
	}))
	t.Cleanup(srv.Close)
	prev := telegramBaseURL
	telegramBaseURL = srv.URL
	t.Cleanup(func() { telegramBaseURL = prev })
	return f
}

func (f *fakeTelegram) setReply(method, reply string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[method] = reply
}

func (f *fakeTelegram) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeTelegram) last(t *testing.T) fakeCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no Bot API call recorded")
	}
	return f.calls[len(f.calls)-1]
}

// withLiveConfig installs cfg as the live config for the test.
func withLiveConfig(t *testing.T, cfg Config) {
	t.Helper()
	prev := getConfig()
	setConfig(cfg.withDefaults())
	t.Cleanup(func() { setConfig(prev) })
}

// protectConfigFile backs up config.json next to the test binary (which
// saveConfig writes) and restores it afterwards.
func protectConfigFile(t *testing.T) string {
	t.Helper()
	exePath, err := os.Executable()
	if err != nil {
		t.Skip("cannot determine executable path")
	}
	configPath := filepath.Join(filepath.Dir(exePath), "config.json")
	origData, origErr := os.ReadFile(configPath)
	t.Cleanup(func() {
		if origErr == nil {
			os.WriteFile(configPath, origData, 0644)
		} else {
			os.Remove(configPath)
		}
	})
	return configPath
}

func telegramCfg(enabled bool, token string) Config {
	return Config{Port: 5001, Telegram: TelegramConfig{
		Enabled: enabled, BotToken: token, ChatID: "42", Lang: "ko", Detail: "full", PCName: "TEST-PC",
	}}
}

func postJSON(path, body string) *http.Request {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	return req
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	return out
}

// ---- /api/telegram/test ------------------------------------------------------

func TestTelegramTestEndpointSendsTestMessage(t *testing.T) {
	f := useFakeTelegram(t)
	withLiveConfig(t, telegramCfg(false, testBotToken)) // enabled is irrelevant for a test send

	w := httptest.NewRecorder()
	handleTelegramTest(w, postJSON("/api/telegram/test", ""))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := decodeBody(t, w); got["status"] != "ok" || got["message"] != "sent" {
		t.Errorf("body = %v", got)
	}
	call := f.last(t)
	if call.method != "sendMessage" || call.token != testBotToken {
		t.Fatalf("call = %+v", call)
	}
	text, _ := call.body["text"].(string)
	if !strings.Contains(text, "<b>테스트 메시지</b>") || !strings.Contains(text, "TEST-PC") {
		t.Errorf("text = %q", text)
	}
	if call.body["chat_id"] != "42" || call.body["parse_mode"] != "HTML" {
		t.Errorf("body = %v", call.body)
	}
	if _, has := call.body["reply_markup"]; has {
		t.Error("system.test must not carry a keyboard")
	}
}

func TestTelegramTestEndpointUsesDecryptedStoredToken(t *testing.T) {
	f := useFakeTelegram(t)
	enc, err := secret.Protect(testBotToken)
	if err != nil {
		t.Fatal(err)
	}
	withLiveConfig(t, telegramCfg(true, enc))

	w := httptest.NewRecorder()
	handleTelegramTest(w, postJSON("/api/telegram/test", ""))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if f.last(t).token != testBotToken {
		t.Errorf("token sent to Telegram = %q, want the decrypted one", f.last(t).token)
	}
}

func TestTelegramTestEndpointOverrideBody(t *testing.T) {
	f := useFakeTelegram(t)
	withLiveConfig(t, telegramCfg(false, "")) // nothing saved yet

	w := httptest.NewRecorder()
	handleTelegramTest(w, postJSON("/api/telegram/test", `{"bot_token":"999:unsaved","chat_id":"77"}`))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	call := f.last(t)
	if call.token != "999:unsaved" || call.body["chat_id"] != "77" {
		t.Errorf("call = %+v", call)
	}

	// The masked placeholder in the body means "use the stored token".
	withLiveConfig(t, telegramCfg(false, testBotToken))
	w = httptest.NewRecorder()
	handleTelegramTest(w, postJSON("/api/telegram/test", `{"bot_token":"****6789"}`))
	if w.Code != 200 || f.last(t).token != testBotToken {
		t.Errorf("status %d, token %q", w.Code, f.last(t).token)
	}
}

func TestTelegramTestEndpointNotConfigured(t *testing.T) {
	f := useFakeTelegram(t)
	withLiveConfig(t, Config{Port: 5001})

	w := httptest.NewRecorder()
	handleTelegramTest(w, postJSON("/api/telegram/test", ""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := decodeBody(t, w); got["status"] != "error" || !strings.Contains(got["message"].(string), "bot token") {
		t.Errorf("body = %v", got)
	}
	if f.count() != 0 {
		t.Error("no Bot API call expected")
	}

	// Token but no chat id.
	withLiveConfig(t, Config{Port: 5001, Telegram: TelegramConfig{BotToken: testBotToken}})
	w = httptest.NewRecorder()
	handleTelegramTest(w, postJSON("/api/telegram/test", ""))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "chat id") {
		t.Errorf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestTelegramTestEndpointReportsAPIError(t *testing.T) {
	f := useFakeTelegram(t)
	f.setReply("sendMessage", `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	withLiveConfig(t, telegramCfg(true, testBotToken))

	w := httptest.NewRecorder()
	handleTelegramTest(w, postJSON("/api/telegram/test", ""))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := decodeBody(t, w); got["status"] != "error" || !strings.Contains(got["message"].(string), "chat not found") {
		t.Errorf("body = %v", got)
	}
}

func TestTelegramEndpointsRequireCSRFAndMethod(t *testing.T) {
	useFakeTelegram(t)
	withLiveConfig(t, telegramCfg(true, testBotToken))

	w := httptest.NewRecorder()
	handleTelegramTest(w, httptest.NewRequest("POST", "/api/telegram/test", nil)) // no X-Requested-With
	if w.Code != http.StatusForbidden {
		t.Errorf("POST without CSRF header: status %d", w.Code)
	}
	w = httptest.NewRecorder()
	handleTelegramTest(w, httptest.NewRequest("GET", "/api/telegram/test", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET test: status %d", w.Code)
	}
	w = httptest.NewRecorder()
	handleTelegramMe(w, postJSON("/api/telegram/me", ""))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST me: status %d", w.Code)
	}

	// With a secret set, an unauthenticated request is rejected before
	// anything else.
	cfg := telegramCfg(true, testBotToken)
	cfg.Secret = "s3cret"
	withLiveConfig(t, cfg)
	w = httptest.NewRecorder()
	handleTelegramMe(w, httptest.NewRequest("GET", "/api/telegram/me", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated me: status %d", w.Code)
	}
}

// ---- /api/telegram/me ---------------------------------------------------------

func TestTelegramMeEndpoint(t *testing.T) {
	f := useFakeTelegram(t)
	withLiveConfig(t, telegramCfg(false, testBotToken))

	w := httptest.NewRecorder()
	handleTelegramMe(w, httptest.NewRequest("GET", "/api/telegram/me", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := decodeBody(t, w)
	if got["status"] != "ok" || got["username"] != "pcbot" || got["name"] != "PC Bot" {
		t.Errorf("body = %v", got)
	}
	if c := f.last(t); c.method != "getMe" || c.token != testBotToken {
		t.Errorf("call = %+v", c)
	}

	withLiveConfig(t, Config{Port: 5001})
	w = httptest.NewRecorder()
	handleTelegramMe(w, httptest.NewRequest("GET", "/api/telegram/me", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("no token: status %d", w.Code)
	}
}

func TestTelegramMeEndpointUnauthorizedToken(t *testing.T) {
	f := useFakeTelegram(t)
	f.setReply("getMe", `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	withLiveConfig(t, telegramCfg(false, "bad:token"))

	w := httptest.NewRecorder()
	handleTelegramMe(w, httptest.NewRequest("GET", "/api/telegram/me", nil))
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := decodeBody(t, w); !strings.Contains(got["message"].(string), "401") {
		t.Errorf("body = %v", got)
	}
}

// ---- /api/telegram/chats ------------------------------------------------------

func TestTelegramChatsEndpointDedups(t *testing.T) {
	f := useFakeTelegram(t)
	withLiveConfig(t, telegramCfg(false, testBotToken))

	w := httptest.NewRecorder()
	handleTelegramChats(w, httptest.NewRequest("GET", "/api/telegram/chats", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		Status string         `json:"status"`
		Chats  []telegramChat `json:"chats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "ok" || len(got.Chats) != 2 {
		t.Fatalf("body = %s", w.Body.String())
	}
	if got.Chats[0] != (telegramChat{ChatID: "42", Title: "Kim Lump", Username: "lump", Type: "private"}) {
		t.Errorf("chats[0] = %+v", got.Chats[0])
	}
	if got.Chats[1] != (telegramChat{ChatID: "-100", Title: "Home", Type: "group"}) {
		t.Errorf("chats[1] = %+v", got.Chats[1])
	}
	c := f.last(t)
	if c.method != "getUpdates" || c.body["offset"] != float64(0) || c.body["timeout"] != float64(0) {
		t.Errorf("call = %+v", c)
	}
}

func TestTelegramChatsEndpointPostOverrideAndEmpty(t *testing.T) {
	f := useFakeTelegram(t)
	f.setReply("getUpdates", `{"ok":true,"result":[]}`)
	withLiveConfig(t, Config{Port: 5001})

	w := httptest.NewRecorder()
	handleTelegramChats(w, postJSON("/api/telegram/chats", `{"bot_token":"555:typed"}`))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if f.last(t).token != "555:typed" {
		t.Errorf("token = %q", f.last(t).token)
	}
	if body := w.Body.String(); !strings.Contains(body, `"chats":[]`) {
		t.Errorf("empty result must encode as []: %s", body)
	}

	w = httptest.NewRecorder()
	handleTelegramChats(w, httptest.NewRequest("GET", "/api/telegram/chats", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("GET without live token: status %d", w.Code)
	}
}

// ---- /api/config token masking -----------------------------------------------------

func TestConfigGetMasksToken(t *testing.T) {
	enc, err := secret.Protect(testBotToken)
	if err != nil {
		t.Fatal(err)
	}
	withLiveConfig(t, telegramCfg(true, enc))

	w := httptest.NewRecorder()
	handleConfigAPI(w, httptest.NewRequest("GET", "/api/config", nil))
	if w.Code != 200 {
		t.Fatalf("status %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, testBotToken) || strings.Contains(body, enc) {
		t.Fatal("GET /api/config leaks the token")
	}
	got := decodeBody(t, w)
	tg, _ := got["telegram"].(map[string]any)
	if tg["bot_token"] != "****6789" || tg["bot_token_set"] != true {
		t.Errorf("telegram = %v", tg)
	}
	if tg["chat_id"] != "42" || tg["enabled"] != true || got["port"] != float64(5001) {
		t.Errorf("other keys lost: %v", got)
	}
	if _, ok := got["notify"].(map[string]any); !ok {
		t.Errorf("notify missing: %v", got)
	}
	// The live config is untouched.
	if getConfig().Telegram.BotToken != enc {
		t.Error("masking modified the live config")
	}

	withLiveConfig(t, telegramCfg(true, ""))
	w = httptest.NewRecorder()
	handleConfigAPI(w, httptest.NewRequest("GET", "/api/config", nil))
	tg, _ = decodeBody(t, w)["telegram"].(map[string]any)
	if tg["bot_token"] != "" || tg["bot_token_set"] != false {
		t.Errorf("empty token view = %v", tg)
	}
}

func TestConfigPostTokenRules(t *testing.T) {
	protectConfigFile(t)
	enc, err := secret.Protect(testBotToken)
	if err != nil {
		t.Fatal(err)
	}
	withLiveConfig(t, telegramCfg(true, enc))

	post := func(t *testing.T, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		handleConfigAPI(w, postJSON("/api/config", body))
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}

	// Masked placeholder echoed back: keep.
	post(t, `{"port":5001,"telegram":{"enabled":true,"bot_token":"****6789","chat_id":"42"}}`)
	if getConfig().Telegram.BotToken != enc {
		t.Error("masked token must keep the stored one")
	}
	// Empty: keep.
	post(t, `{"port":5001,"telegram":{"enabled":true,"bot_token":"","chat_id":"43"}}`)
	if got := getConfig().Telegram; got.BotToken != enc || got.ChatID != "43" {
		t.Errorf("empty token must keep the stored one: %+v", got)
	}
	// Omitted key (older GUI): keep.
	post(t, `{"port":5001}`)
	if getConfig().Telegram.BotToken != enc {
		t.Error("omitted telegram must keep the stored token")
	}
	// New plaintext: replaced and stored protected.
	post(t, `{"port":5001,"telegram":{"bot_token":"111:newtoken0000","chat_id":"43"}}`)
	stored := getConfig().Telegram.BotToken
	if !secret.IsProtected(stored) || stored == enc {
		t.Fatalf("new token must be stored protected: %q", stored)
	}
	if plain, _ := secret.Unprotect(stored); plain != "111:newtoken0000" {
		t.Errorf("decrypts to %q", plain)
	}
	// "-": clear.
	post(t, `{"port":5001,"telegram":{"bot_token":"-"}}`)
	if got := getConfig().Telegram.BotToken; got != "" {
		t.Errorf("\"-\" must clear the token, got %q", got)
	}
}

func TestConfigChangedKeysListsTokenOnlyWhenReplaced(t *testing.T) {
	enc, err := secret.Protect(testBotToken)
	if err != nil {
		t.Fatal(err)
	}
	old := telegramCfg(true, enc).withDefaults()

	kept := normalizeConfig(func() Config { c := old.forUpdate(); c.Telegram.BotToken = "****6789"; return c }(), old)
	for _, k := range configChangedKeys(old, kept) {
		if k == "telegram.bot_token" {
			t.Error("kept token reported as changed")
		}
	}
	replaced := normalizeConfig(func() Config { c := old.forUpdate(); c.Telegram.BotToken = "222:other"; return c }(), old)
	keys := strings.Join(configChangedKeys(old, replaced), ",")
	if !strings.Contains(keys, "telegram.bot_token") {
		t.Errorf("replaced token not reported: %s", keys)
	}
	if strings.Contains(keys, "222:other") || strings.Contains(keys, testBotToken) {
		t.Error("changed keys must not carry values")
	}
}

// ---- saveConfig / loadConfig encryption (#65) --------------------------------------

func TestSaveConfigEncryptsTokenAndLoadDecrypts(t *testing.T) {
	configPath := protectConfigFile(t)
	withLiveConfig(t, Config{Port: 5001})

	if err := saveConfig(telegramCfg(true, testBotToken)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), testBotToken) {
		t.Fatal("config.json contains the plaintext token")
	}
	var raw struct {
		Telegram struct {
			BotToken string `json:"bot_token"`
		} `json:"telegram"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if !secret.IsProtected(raw.Telegram.BotToken) {
		t.Fatalf("on-disk token not protected: %q", raw.Telegram.BotToken)
	}
	// In-memory copy carries the same protected value.
	if getConfig().Telegram.BotToken != raw.Telegram.BotToken {
		t.Error("live config differs from disk")
	}

	loaded := loadConfig()
	if loaded.Telegram.BotToken != raw.Telegram.BotToken {
		t.Errorf("loadConfig must keep the stored form, got %q", loaded.Telegram.BotToken)
	}
	if plain, err := liveBotToken(loaded.Telegram); err != nil || plain != testBotToken {
		t.Errorf("decrypt = %q, %v", plain, err)
	}

	// Saving again must not re-encrypt (ciphertext stays stable).
	if err := saveConfig(loaded); err != nil {
		t.Fatal(err)
	}
	if getConfig().Telegram.BotToken != raw.Telegram.BotToken {
		t.Error("already protected token was re-encrypted")
	}
}

func TestLoadConfigBlanksUndecryptableToken(t *testing.T) {
	configPath := protectConfigFile(t)
	// Valid base64, but not a DPAPI blob from this machine.
	bad := `{"port":5001,"telegram":{"enabled":true,"bot_token":"dpapi:AQIDBAUGBwgJCgsMDQ4PEA==","chat_id":"42"}}`
	if err := os.WriteFile(configPath, []byte(bad), 0644); err != nil {
		t.Fatal(err)
	}
	loaded := loadConfig()
	if loaded.Telegram.BotToken != "" {
		t.Errorf("undecryptable token must be blanked, got %q", loaded.Telegram.BotToken)
	}
	if loaded.Port != 5001 || loaded.Telegram.ChatID != "42" || !loaded.Telegram.Enabled {
		t.Errorf("rest of the config must survive: %+v", loaded)
	}
}

// ---- liveSink ----------------------------------------------------------------------

func TestLiveSinkDropsWhenDisabledAndSendsWhenEnabled(t *testing.T) {
	f := useFakeTelegram(t)
	withLiveConfig(t, telegramCfg(false, testBotToken))
	s := newLiveSink()
	ev := notify.Event{Category: "system", Kind: "test", Fields: map[string]string{}}

	if err := s.Send(context.Background(), ev); err != nil {
		t.Fatalf("disabled Send: %v", err)
	}
	if f.count() != 0 {
		t.Fatal("disabled sink must not call Telegram")
	}

	// Missing chat id is also "not configured".
	cfg := telegramCfg(true, testBotToken)
	cfg.Telegram.ChatID = ""
	withLiveConfig(t, cfg)
	if err := s.Send(context.Background(), ev); err != nil || f.count() != 0 {
		t.Fatalf("incomplete config: err=%v calls=%d", err, f.count())
	}

	var gotID int
	s.SetOnSent(func(_ notify.Event, id int) { gotID = id })
	enc, _ := secret.Protect(testBotToken)
	withLiveConfig(t, telegramCfg(true, enc))
	if err := s.Send(context.Background(), ev); err != nil {
		t.Fatalf("enabled Send: %v", err)
	}
	c := f.last(t)
	if c.method != "sendMessage" || c.token != testBotToken || c.body["chat_id"] != "42" {
		t.Errorf("call = %+v", c)
	}
	if text, _ := c.body["text"].(string); !strings.Contains(text, "TEST-PC") {
		t.Errorf("text = %q", text)
	}
	if gotID != 7 {
		t.Errorf("OnSent id = %d, want 7", gotID)
	}
}

func TestLiveSinkRebuildsOnConfigChange(t *testing.T) {
	f := useFakeTelegram(t)
	withLiveConfig(t, telegramCfg(true, testBotToken))
	s := newLiveSink()
	ev := notify.Event{Category: "system", Kind: "test", Fields: map[string]string{}}

	if err := s.Send(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	first := s.inner
	if err := s.Send(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if s.inner != first {
		t.Error("unchanged config must reuse the inner sink")
	}

	cfg := telegramCfg(true, "777:rotated")
	cfg.Telegram.Lang = "en"
	cfg.Telegram.ChatID = "99"
	withLiveConfig(t, cfg)
	var gotEv notify.Event
	s.SetOnSent(func(e notify.Event, _ int) { gotEv = e })
	if err := s.Send(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if s.inner == first {
		t.Error("token change must rebuild the inner sink")
	}
	c := f.last(t)
	if c.token != "777:rotated" || c.body["chat_id"] != "99" {
		t.Errorf("call = %+v", c)
	}
	if text, _ := c.body["text"].(string); !strings.Contains(text, "Test message") {
		t.Errorf("lang change not applied: %q", text)
	}
	if gotEv.Key() != "system.test" {
		t.Error("OnSent must be re-applied to the rebuilt sink")
	}
}

func TestLiveSinkPropagatesRateLimit(t *testing.T) {
	f := useFakeTelegram(t)
	f.setReply("sendMessage", `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":3}}`)
	withLiveConfig(t, telegramCfg(true, testBotToken))
	s := newLiveSink()
	err := s.Send(context.Background(), notify.Event{Category: "system", Kind: "test"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if ra, ok := telegram.RetryAfterOf(err); !ok || ra.Seconds() != 3 {
		t.Errorf("RateLimitError not propagated: %v", err)
	}
}
