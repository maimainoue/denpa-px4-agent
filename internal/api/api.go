// Package api は denpa との HTTPS の API (仕様書 10 章)。
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/backend"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/config"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/logx"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/manager"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/store"
	"github.com/denpa-px4-agent/denpa-px4-agent/internal/tlsx"
)

// Card はカードと復号の窓口 (9 章)。nil なら、カードは使えない。
type Card interface {
	// Status は GET /denpa/card の本文。
	Status(ctx context.Context) map[string]any
	// SetDisabled は PUT /denpa/card/readers。
	SetDisabled(names []string) ([]string, error)
	// Confirm は POST /denpa/card/confirm。
	Confirm(ctx context.Context, verification []byte, mjd int) (code int, err error)
	// DecodeFile は POST /denpa/decode。置き場の外・リンクは呼ぶ側が断る。
	DecodeFile(in, out string) (map[string]any, error)
}

// Deps は API が使うもの。
type Deps struct {
	Cfg      *config.Config
	Log      *logx.Logger
	ID       *tlsx.Identity
	Mgr      *manager.Manager
	Channels *store.Channels
	Tuners   store.Tuners
	Hub      *Hub
	Card     Card
	// ValidateDevice は device 文字列の検査 (`cmd:` の機種があるか)。空なら通す。
	ValidateDevice func(device string) error
	// Redetect は定義を消したあとに、自動検出に戻す。
	Redetect func()
}

// Server は /denpa/* の窓口。
type Server struct {
	d Deps
}

// New は Server を作る。
func New(d Deps) *Server { return &Server{d: d} }

// WriteJSON は日本語をエスケープせずに書く。
func WriteJSON(w http.ResponseWriter, code int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	w.Write(buf.Bytes())
}

type errBody struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func fail(w http.ResponseWriter, code int, msg string, args ...any) {
	WriteJSON(w, code, errBody{Error: fmt.Sprintf(msg, args...)})
}

func failErr(w http.ResponseWriter, err error) {
	fail(w, backend.StatusOf(err), "%s", err.Error())
}

// Handler は全部の経路。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/denpa/stream", get(s.stream))
	mux.HandleFunc("/denpa/events", get(s.events))
	mux.HandleFunc("/denpa/tuners", s.tuners)
	mux.HandleFunc("/denpa/channels", s.channels)
	mux.HandleFunc("/denpa/card", get(s.card))
	mux.HandleFunc("/denpa/card/readers", method("PUT", s.cardReaders))
	mux.HandleFunc("/denpa/card/confirm", method("POST", s.cardConfirm))
	mux.HandleFunc("/denpa/decode", method("POST", s.decode))
	mux.HandleFunc("/denpa/raw/probe", method("POST", s.rawProbe))
	mux.HandleFunc("/denpa/pair", s.pair)
	mux.HandleFunc("/denpa/logs", get(s.logs))
	mux.HandleFunc("/denpa/about", get(s.about))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { fail(w, 404, "not found") })

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fp := tlsx.PeerFingerprint(r)
		paired := fp != "" && s.d.ID.Paired(fp)
		if !paired && !(r.Method == http.MethodPost && r.URL.Path == "/denpa/pair") {
			tlsx.RejectUnpaired(w)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func method(m string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != m {
			fail(w, 404, "not found")
			return
		}
		h(w, r)
	}
}

func get(h http.HandlerFunc) http.HandlerFunc { return method(http.MethodGet, h) }

func (s *Server) pair(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.d.ID.HandlePair(w, r)
	case http.MethodDelete:
		s.d.ID.HandleUnpair(w, r)
	default:
		fail(w, 404, "not found")
	}
}

// ---- stream ----

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	req := manager.Request{
		Type: q.Get("type"), Channel: q.Get("channel"), Use: q.Get("use"),
		After: q["after"], Pin: q.Get("tuner"),
	}
	if p := q.Get("priority"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil {
			fail(w, 400, "priority は整数にします")
			return
		}
		req.Priority = n
	}
	if sv := q.Get("service"); sv != "" {
		n, err := strconv.Atoi(sv)
		if err != nil || n < 1 || n > 65535 {
			fail(w, 400, "service は 1〜65535 の番組番号です")
			return
		}
		req.Service = n
	}
	sink, err := s.d.Mgr.Open(req)
	if err != nil {
		failErr(w, err)
		return
	}
	defer sink.Release()
	go func() {
		<-r.Context().Done()
		sink.Release()
	}()

	rc := http.NewResponseController(w)
	sent := false
	for {
		b, err := sink.Read()
		if err != nil {
			if errors.Is(err, manager.ErrKilled) {
				if !sent {
					msg := sink.Reason()
					if msg == "" {
						msg = "選局が終わりました"
					}
					fail(w, 409, "%s", msg)
					return
				}
				panic(http.ErrAbortHandler) // 正常終了にしない。denpa が録り終えたと読んでしまう
			}
			return
		}
		if !sent {
			sent = true
			ct := "video/MP2T"
			if sink.TLV() {
				ct = "application/x-arib-tlv"
			}
			w.Header().Set("Content-Type", ct)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(200)
		}
		if _, err := w.Write(b); err != nil {
			return
		}
		rc.Flush()
	}
}

// ---- events ----

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	// ヘッダだけ先に送る (本文は最初の通知まで空)。最初の読み取りが通知になる
	rc.Flush()
	ch, cancel := s.d.Hub.Subscribe()
	defer cancel()
	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: {}\n\n", ev)
			rc.Flush()
		case <-tk.C:
			w.Write([]byte(": keep-alive\n\n"))
			rc.Flush()
		}
	}
}

// ---- tuners ----

func (s *Server) tunersBody() map[string]any {
	return map[string]any{"tuners": s.d.Mgr.Snapshot(), "detected": s.d.Mgr.Detected()}
}

func (s *Server) tuners(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		WriteJSON(w, 200, s.tunersBody())
	case http.MethodPut:
		s.putTuners(w, r)
	default:
		fail(w, 404, "not found")
	}
}

func (s *Server) putTuners(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Tuners []map[string]any `json:"tuners"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil || body.Tuners == nil {
		fail(w, 400, "tuners が必要です")
		return
	}
	specs := make([]backend.TunerSpec, 0, len(body.Tuners))
	for _, it := range body.Tuners {
		name, _ := it["name"].(string)
		if name == "" {
			fail(w, 400, "name の無いチューナーがあります")
			return
		}
		spec := backend.TunerSpec{Name: name, Types: []string{}}
		if v, ok := it["types"]; ok {
			arr, isArr := v.([]any)
			if !isArr {
				fail(w, 400, "%s: types は文字列の配列にします", name)
				return
			}
			for _, e := range arr {
				str, isStr := e.(string)
				if !isStr {
					fail(w, 400, "%s: types は文字列の配列にします", name)
					return
				}
				spec.Types = append(spec.Types, str)
			}
		}
		if v, ok := it["device"]; ok && v != nil {
			str, isStr := v.(string)
			if !isStr {
				fail(w, 400, "%s: device は文字列にします", name)
				return
			}
			spec.Device = str
		}
		if v, ok := it["lnb"]; ok && v != nil {
			str, isStr := v.(string)
			if !isStr {
				fail(w, 400, "%s: lnb は文字列にします", name)
				return
			}
			spec.Lnb = str
		}
		if v, ok := it["disabled"]; ok && v != nil {
			b, isBool := v.(bool)
			if !isBool {
				fail(w, 400, "%s: disabled は真偽値にします", name)
				return
			}
			spec.Disabled = b
		}
		if spec.Device != "" {
			okPrefix := false
			for _, p := range []string{"px4:", "px4host:", "cmd:"} {
				if strings.HasPrefix(spec.Device, p) {
					okPrefix = true
				}
			}
			if !okPrefix {
				fail(w, 400, "%s: device は px4:・px4host:・cmd: で始まる形にします", name)
				return
			}
			if s.d.ValidateDevice != nil {
				if err := s.d.ValidateDevice(spec.Device); err != nil {
					fail(w, 400, "%s: %s", name, err.Error())
					return
				}
			}
		}
		specs = append(specs, spec)
	}
	if len(specs) == 0 {
		if err := s.d.Tuners.Remove(); err != nil {
			fail(w, 500, "tuners.json を消せません: %v", err)
			return
		}
		s.d.Log.Printf("チューナーの定義を消しました (自動検出に戻します)")
		if s.d.Redetect != nil {
			s.d.Redetect()
		} else {
			s.d.Mgr.SetSpecs(nil, true)
		}
	} else {
		if err := s.d.Tuners.Save(specs); err != nil {
			fail(w, 500, "tuners.json を書けません: %v", err)
			return
		}
		s.d.Log.Printf("チューナーの定義を保存しました (%d 台)", len(specs))
		s.d.Mgr.SetSpecs(specs, false)
	}
	WriteJSON(w, 200, s.tunersBody())
}

// ---- channels ----

func (s *Server) channels(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		WriteJSON(w, 200, s.d.Channels.List())
	case http.MethodPut:
		var body struct {
			Channels []store.Channel `json:"channels"`
			Scanned  []any           `json:"scanned"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, 32<<20))
		dec.UseNumber()
		if err := dec.Decode(&body); err != nil || body.Channels == nil || len(body.Scanned) == 0 {
			fail(w, 400, "channels と scanned が必要です")
			return
		}
		if len(body.Channels) == 0 {
			fail(w, 400, "チャンネルが 1 件もありません")
			return
		}
		var scanned []string
		for _, v := range body.Scanned {
			str, ok := v.(string)
			if !ok {
				fail(w, 400, "scanned と、channels の type・channel は文字列で書きます")
				return
			}
			scanned = append(scanned, str)
		}
		for _, ch := range body.Channels {
			_, a := ch["type"].(string)
			_, b := ch["channel"].(string)
			if !a || !b {
				fail(w, 400, "scanned と、channels の type・channel は文字列で書きます")
				return
			}
		}
		out, err := s.d.Channels.Replace(body.Channels, scanned)
		if err != nil {
			fail(w, 500, "channels.json を書けません: %v", err)
			return
		}
		s.d.Log.Printf("チャンネルを保存しました (%s: %d 件)", strings.Join(scanned, "・"), len(body.Channels))
		s.d.Hub.Notify("channels")
		WriteJSON(w, 200, out)
	case http.MethodDelete:
		typ := r.URL.Query().Get("type")
		if typ == "" {
			fail(w, 400, "type が必要です")
			return
		}
		out, err := s.d.Channels.DeleteType(typ)
		if err != nil {
			fail(w, 500, "channels.json を書けません: %v", err)
			return
		}
		s.d.Log.Printf("チャンネルを消しました (%s)", typ)
		s.d.Hub.Notify("channels")
		WriteJSON(w, 200, out)
	default:
		fail(w, 404, "not found")
	}
}

// ---- card ----

func (s *Server) card(w http.ResponseWriter, r *http.Request) {
	if s.d.Card == nil {
		WriteJSON(w, 200, map[string]any{"ok": false, "message": "カードを使えません", "source": "local", "readers": []any{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 9*time.Second)
	defer cancel()
	WriteJSON(w, 200, s.d.Card.Status(ctx))
}

func (s *Server) cardReaders(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled []string `json:"disabled"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil {
		fail(w, 400, "disabled が必要です")
		return
	}
	if s.d.Card == nil {
		fail(w, 500, "カードを使えません")
		return
	}
	out, err := s.d.Card.SetDisabled(body.Disabled)
	if err != nil {
		fail(w, 500, "%v", err)
		return
	}
	WriteJSON(w, 200, map[string]any{"disabled": out})
}

func (s *Server) cardConfirm(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Verification string `json:"verification"`
		Date         int    `json:"date"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil {
		fail(w, 400, "verification と date が必要です")
		return
	}
	raw, err := decodeBase64(body.Verification)
	if err != nil || len(raw) < 1 || len(raw) > 253 {
		fail(w, 400, "verification は Base64 の 1〜253 バイトにします")
		return
	}
	if body.Date < 0 || body.Date > 0xFFFF {
		fail(w, 400, "date は MJD (0〜65535) にします")
		return
	}
	if s.d.Card == nil {
		fail(w, 500, "カードを使えません")
		return
	}
	code, err := s.d.Card.Confirm(r.Context(), raw, body.Date)
	if err != nil {
		fail(w, 500, "%v", err)
		return
	}
	WriteJSON(w, 200, map[string]any{"ok": true, "code": code})
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Input  string `json:"input"`
		Output string `json:"output"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || body.Input == "" || body.Output == "" {
		fail(w, 400, "input と output が必要です")
		return
	}
	in, err := s.rawPath(body.Input)
	if err != nil {
		fail(w, 500, "input: %v", err)
		return
	}
	out, err := s.rawPath(body.Output)
	if err != nil {
		fail(w, 500, "output: %v", err)
		return
	}
	if _, err := os.Lstat(out); err == nil {
		fail(w, 500, "書き出す先がもうあります (%s)", body.Output)
		return
	}
	if s.d.Card == nil {
		fail(w, 500, "カードを使えません")
		return
	}
	res, err := s.d.Card.DecodeFile(in, out)
	if err != nil {
		os.Remove(out)
		fail(w, 500, "%v", err)
		return
	}
	res["ok"] = true
	WriteJSON(w, 200, res)
}

// rawPath は置き場からの相対パスを絶対にする。置き場の外・リンク・`..` は断る。
func (s *Server) rawPath(rel string) (string, error) {
	if filepath.IsAbs(rel) || strings.Contains(filepath.ToSlash(rel), "..") {
		return "", errors.New("保存先の外は使えません (置き場からの相対パスにします)")
	}
	root, err := filepath.Abs(s.d.Cfg.RawDir)
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	if p != root && !strings.HasPrefix(p, root+string(filepath.Separator)) {
		return "", errors.New("保存先の外です")
	}
	// 途中にリンクがあれば断る
	cur := root
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		cur = filepath.Join(cur, part)
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("リンクは使えません")
		}
	}
	return p, nil
}

func (s *Server) rawProbe(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body) != nil || !strings.HasPrefix(body.Path, ".denpa/") {
		fail(w, 400, "path は .denpa/ の下にします")
		return
	}
	p, err := s.rawPath(body.Path)
	if err != nil {
		fail(w, 400, "%v", err)
		return
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		fail(w, 404, "ファイルがありません")
		return
	}
	if err != nil || len(data) > 4096 {
		fail(w, 400, "読めません (4 KiB までのファイルにします)")
		return
	}
	sum := sha256.Sum256(data)
	WriteJSON(w, 200, map[string]any{"ok": true, "sha256": hex.EncodeToString(sum[:])})
}

// ---- logs / about ----

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, 200, map[string]any{"lines": s.d.Log.Lines()})
}

func (s *Server) about(w http.ResponseWriter, r *http.Request) {
	osName := map[string]string{"darwin": "macos"}[runtime.GOOS]
	if osName == "" {
		osName = runtime.GOOS
	}
	arch := map[string]string{"amd64": "x64"}[runtime.GOARCH]
	if arch == "" {
		arch = runtime.GOARCH
	}
	WriteJSON(w, 200, map[string]any{
		"name": s.d.Cfg.Name, "version": s.d.Cfg.Version, "os": osName, "arch": arch,
		"runtime": runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH, "ccidDriver": nil,
	})
}
