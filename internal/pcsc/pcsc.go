// Package pcsc は pcscd (pcsc-lite) の socket に直接つなぐ、pure Go のクライアント (仕様書 9.1)。
//
// プロトコルは pcsc-lite 1.9 の winscard_msg.h と winscard_clnt.c のとおり (バージョン 4.4)。
// cgo を使わないので、静的なバイナリのまま distroless に置ける。使うのは
// CMD_VERSION・ESTABLISH_CONTEXT・CMD_GET_READERS_STATE・CONNECT・TRANSMIT・DISCONNECT・RELEASE_CONTEXT だけ。
//
// メッセージは `{size u32, command u32}` のヘッダに続けて構造体を送る。サーバの返事はヘッダが無く、構造体がそのまま返る。
// 数はホストのバイト順 (pcscd と同じ機械で動く前提)。
package pcsc

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
)

// DefaultSocket は pcscd の socket。環境変数 PCSCLITE_CSOCK_NAME で変える。
const DefaultSocket = "/run/pcscd/pcscd.comm"

// pcsc-lite のコマンドと定数 (winscard_msg.h・pcsclite.h)。
const (
	protoMajor = 4
	protoMinor = 4

	cmdEstablishContext = 0x01
	cmdReleaseContext   = 0x02
	cmdConnect          = 0x04
	cmdDisconnect       = 0x06
	cmdTransmit         = 0x09
	cmdVersion          = 0x11
	cmdGetReadersState  = 0x12

	scopeSystem   = 2
	shareExcl     = 1
	protoT1       = 2
	leaveCard     = 0
	maxReaderName = 128
	maxATR        = 33
	maxReaders    = 16
	maxBuffer     = 264 // MAX_BUFFER_SIZE (短い APDU)

	// READER_STATE (eventhandler.h): name[128] + u32 ×3 + atr[33] (+3 詰め物) + u32 ×2
	readerStateSize = 184

	ioTimeout = 10 * time.Second
)

var order = binary.NativeEndian

// 返り値 (pcsclite.h)。
const (
	rvOK             = 0x00000000
	rvInsufficient   = 0x80100008
	rvSharing        = 0x8010000B
	rvNoSmartcard    = 0x8010000C
	rvProtoMismatch  = 0x8010000F
	rvReaderGone     = 0x80100017
	rvNoService      = 0x8010001D
	rvUnknownReader  = 0x80100009
	rvRemovedCard    = 0x80100069
	rvUnresponsive   = 0x80100066
	rvCommError      = 0x80100013
	rvNoReadersAvail = 0x8010002E
)

var rvNames = map[uint32]string{
	rvInsufficient:   "SCARD_E_INSUFFICIENT_BUFFER",
	rvSharing:        "SCARD_E_SHARING_VIOLATION",
	rvNoSmartcard:    "SCARD_E_NO_SMARTCARD",
	rvProtoMismatch:  "SCARD_E_PROTO_MISMATCH",
	rvReaderGone:     "SCARD_E_READER_UNAVAILABLE",
	rvNoService:      "SCARD_E_NO_SERVICE",
	rvUnknownReader:  "SCARD_E_UNKNOWN_READER",
	rvRemovedCard:    "SCARD_W_REMOVED_CARD",
	rvUnresponsive:   "SCARD_W_UNRESPONSIVE_CARD",
	rvCommError:      "SCARD_F_COMM_ERROR",
	rvNoReadersAvail: "SCARD_E_NO_READERS_AVAILABLE",
}

// Error は pcscd が返した返り値。
type Error struct {
	Op string
	RV uint32
}

func (e *Error) Error() string {
	if n, ok := rvNames[e.RV]; ok {
		return fmt.Sprintf("pcscd の%sが失敗しました (%s)", e.Op, n)
	}
	return fmt.Sprintf("pcscd の%sが失敗しました (0x%08X)", e.Op, e.RV)
}

// conn は pcscd への接続 1 本。ESTABLISH_CONTEXT のあとの命令は、同じ接続で送る。
type conn struct {
	c    net.Conn
	sock string
	ctx  uint32
}

func dial(sock string) (*conn, error) {
	c, err := net.DialTimeout("unix", sock, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("pcscd に繋がりません (%s)", sock)
	}
	k := &conn{c: c, sock: sock}
	if err := k.handshake(); err != nil {
		c.Close()
		return nil, err
	}
	return k, nil
}

func (k *conn) close() { k.c.Close() }

func (k *conn) send(cmd uint32, body []byte, extra []byte) error {
	buf := make([]byte, 8, 8+len(body)+len(extra))
	order.PutUint32(buf[0:], uint32(len(body)))
	order.PutUint32(buf[4:], cmd)
	buf = append(buf, body...)
	buf = append(buf, extra...)
	k.c.SetWriteDeadline(time.Now().Add(ioTimeout))
	_, err := k.c.Write(buf)
	return err
}

func (k *conn) recv(n int) ([]byte, error) {
	b := make([]byte, n)
	k.c.SetReadDeadline(time.Now().Add(ioTimeout))
	if _, err := io.ReadFull(k.c, b); err != nil {
		return nil, err
	}
	return b, nil
}

// handshake は CMD_VERSION でバージョンを合わせ、ESTABLISH_CONTEXT でコンテキストを得る。
func (k *conn) handshake() error {
	ver := make([]byte, 12)
	order.PutUint32(ver[0:], protoMajor)
	order.PutUint32(ver[4:], protoMinor)
	if err := k.send(cmdVersion, ver, nil); err != nil {
		return err
	}
	r, err := k.recv(12)
	if err != nil {
		return fmt.Errorf("pcscd がバージョンに答えません: %w", err)
	}
	if rv := order.Uint32(r[8:]); rv != rvOK {
		return &Error{Op: "バージョンの確認", RV: rv}
	}
	if major := order.Uint32(r[0:]); major != protoMajor {
		return fmt.Errorf("pcscd のプロトコルのバージョンが違います (%d.%d。%d.%d が必要です)", major, order.Uint32(r[4:]), protoMajor, protoMinor)
	}

	est := make([]byte, 12)
	order.PutUint32(est[0:], scopeSystem)
	if err := k.send(cmdEstablishContext, est, nil); err != nil {
		return err
	}
	if r, err = k.recv(12); err != nil {
		return err
	}
	if rv := order.Uint32(r[8:]); rv != rvOK {
		return &Error{Op: "コンテキストの確立", RV: rv}
	}
	k.ctx = order.Uint32(r[4:])
	return nil
}

// Provider は pcscd 経由のカードリーダー。
type Provider struct {
	Socket string
}

var _ card.Provider = (*Provider)(nil)

// New は socket を指して Provider を作る。空なら DefaultSocket。
func New(socket string) *Provider {
	if socket == "" {
		socket = DefaultSocket
	}
	return &Provider{Socket: socket}
}

// internalName は px4-drv-pcsc の内蔵リーダーの名前の形。`<機材名> Smart Card Reader`、機種によって
// `[ID: …]` と `(<シリアル>)` が付き、pcscd が最後に `<番号> <番号>` を足す。
// libccid の汎用リーダー (`Generic Smart Card Reader Interface 00 00`) は、Reader のあとに Interface が続くので当たらない。
var internalName = regexp.MustCompile(`Smart Card Reader(?: \[ID: [^\]]*\])?(?: \([^)]*\))? \d+ \d+$`)

// IsInternal は px4-drv-pcsc の内蔵リーダーか (9.1)。
func IsInternal(name string) bool {
	return strings.Contains(name, "[ID:") || internalName.MatchString(name)
}

// List は pcscd が持つリーダーを並べる。名前は取ってきたものをそのまま使う。
func (p *Provider) List() ([]card.ReaderInfo, error) {
	k, err := dial(p.Socket)
	if err != nil {
		return nil, err
	}
	defer k.close()
	if err := k.send(cmdGetReadersState, nil, nil); err != nil {
		return nil, err
	}
	b, err := k.recv(maxReaders * readerStateSize)
	if err != nil {
		return nil, fmt.Errorf("リーダーの一覧を読めません: %w", err)
	}
	var out []card.ReaderInfo
	for i := 0; i < maxReaders; i++ {
		e := b[i*readerStateSize : (i+1)*readerStateSize]
		name := cstring(e[:maxReaderName])
		if name == "" {
			continue
		}
		out = append(out, card.ReaderInfo{Name: name, Internal: IsInternal(name)})
	}
	return out, nil
}

func cstring(b []byte) string {
	if i := indexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// Open はリーダーを排他 (SCARD_SHARE_EXCLUSIVE)・T=1 で開く。カードが無ければエラー。
func (p *Provider) Open(name string) (card.Link, error) {
	if len(name) >= maxReaderName {
		return nil, errors.New("リーダーの名前が長すぎます")
	}
	k, err := dial(p.Socket)
	if err != nil {
		return nil, err
	}
	// connect_struct: hContext, szReader[128], dwShareMode, dwPreferredProtocols, hCard, dwActiveProtocol, rv
	req := make([]byte, 4+maxReaderName+20)
	order.PutUint32(req[0:], k.ctx)
	copy(req[4:4+maxReaderName], name)
	order.PutUint32(req[4+maxReaderName:], shareExcl)
	order.PutUint32(req[8+maxReaderName:], protoT1)
	if err := k.send(cmdConnect, req, nil); err != nil {
		k.close()
		return nil, err
	}
	r, err := k.recv(len(req))
	if err != nil {
		k.close()
		return nil, err
	}
	// 返事: hCard は 140、dwActiveProtocol は 144、rv は 148
	const base = 4 + maxReaderName
	if rv := order.Uint32(r[base+16:]); rv != rvOK {
		k.close()
		return nil, &Error{Op: "カードへの接続", RV: rv}
	}
	return &link{k: k, card: int32(order.Uint32(r[base+8:])), proto: order.Uint32(r[base+12:])}, nil
}

// link は開いたカード。
type link struct {
	mu    sync.Mutex
	k     *conn
	card  int32
	proto uint32
}

// Transmit は APDU を送って応答 (SW1 SW2 込み) を受ける。
//
// transmit_struct: hCard, ioSendPciProtocol, ioSendPciLength, cbSendLength,
// ioRecvPciProtocol, ioRecvPciLength, pcbRecvLength, rv (8 × u32 = 32 バイト)。
// 構造体のあとに送るバッファが続き、返事は構造体のあとに受信データが続く。
func (l *link) Transmit(apdu []byte) ([]byte, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.k == nil {
		return nil, errors.New("カードを閉じています")
	}
	if len(apdu) == 0 || len(apdu) > maxBuffer {
		return nil, fmt.Errorf("APDU の長さが範囲の外です (%d バイト)", len(apdu))
	}
	req := make([]byte, 32)
	order.PutUint32(req[0:], uint32(l.card))
	order.PutUint32(req[4:], l.proto) // ioSendPciProtocol (SCARD_IO_REQUEST.dwProtocol)
	order.PutUint32(req[8:], 8)       // ioSendPciLength (sizeof(SCARD_IO_REQUEST))
	order.PutUint32(req[12:], uint32(len(apdu)))
	order.PutUint32(req[16:], 3) // ioRecvPciProtocol (SCARD_PROTOCOL_ANY)
	order.PutUint32(req[20:], 8) // ioRecvPciLength
	order.PutUint32(req[24:], maxBuffer)
	if err := l.k.send(cmdTransmit, req, apdu); err != nil {
		return nil, err
	}
	r, err := l.k.recv(32)
	if err != nil {
		return nil, err
	}
	if rv := order.Uint32(r[28:]); rv != rvOK {
		return nil, &Error{Op: "APDU の送信", RV: rv}
	}
	n := int(order.Uint32(r[24:]))
	if n > maxBuffer {
		return nil, &Error{Op: "APDU の送信", RV: rvInsufficient}
	}
	return l.k.recv(n)
}

// Close は DISCONNECT と RELEASE_CONTEXT を送って、接続を閉じる。
func (l *link) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.k == nil {
		return nil
	}
	k := l.k
	l.k = nil
	// disconnect_struct: hCard, dwDisposition, rv
	d := make([]byte, 12)
	order.PutUint32(d[0:], uint32(l.card))
	order.PutUint32(d[4:], leaveCard)
	if k.send(cmdDisconnect, d, nil) == nil {
		k.recv(12)
	}
	// release_struct: hContext, rv
	rel := make([]byte, 8)
	order.PutUint32(rel[0:], k.ctx)
	if k.send(cmdReleaseContext, rel, nil) == nil {
		k.recv(8)
	}
	k.close()
	return nil
}
