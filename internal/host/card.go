package host

import (
	"fmt"
	"sync"

	"github.com/denpa-px4-agent/denpa-px4-agent/internal/card"
)

// CardProvider は DriverHost_PX4 のカードのパイプ。内蔵リーダーを持つ (9.1)。
type CardProvider struct {
	tr Transport
}

// NewCardProvider はカードのパイプの Provider を作る。
func NewCardProvider(tr Transport) *CardProvider { return &CardProvider{tr: tr} }

var _ card.Provider = (*CardProvider)(nil)

func cardErr(l Layout, resp []byte) error {
	if e := l.cardError(resp); e != cardErrNone {
		if n, ok := cardErrNames[e]; ok {
			return fmt.Errorf("カードのパイプがエラーを返しました (%s)", n)
		}
		return fmt.Errorf("カードのパイプがエラーを返しました (%d)", e)
	}
	return nil
}

// dialCard はカードのパイプに繋いで、バージョンを確かめる。
func (p *CardProvider) dialCard() (*rpc, error) {
	c, err := p.tr.Dial(CardPipe)
	if err != nil {
		return nil, err
	}
	r := &rpc{conn: c}
	l := p.tr.Layout()
	resp, err := r.call(l.cardReq(cardGetVersion))
	if err != nil {
		r.close()
		return nil, err
	}
	if v := l.cardVersion(resp); v != cardVersionValue {
		r.close()
		return nil, fmt.Errorf("DriverHost_PX4 のカードのパイプのバージョンが違います (0x%08X)", v)
	}
	return r, nil
}

// List は内蔵リーダーを並べる。
func (p *CardProvider) List() ([]card.ReaderInfo, error) {
	r, err := p.dialCard()
	if err != nil {
		return nil, err
	}
	defer r.close()
	l := p.tr.Layout()
	resp, err := r.call(l.cardReq(cardListReaders))
	if err != nil {
		return nil, err
	}
	if err := cardErr(l, resp); err != nil {
		return nil, err
	}
	var out []card.ReaderInfo
	for _, n := range l.cardReaders(resp) {
		out = append(out, card.ReaderInfo{Name: n, Internal: true})
	}
	return out, nil
}

// Open は排他 (EXCLUSIVE) で開く。カードが無ければ NO_CARD。
func (p *CardProvider) Open(name string) (card.Link, error) {
	r, err := p.dialCard()
	if err != nil {
		return nil, err
	}
	l := p.tr.Layout()
	req := l.cardReq(cardConnect)
	l.setReaderName(req, name)
	l.setShareMode(req, cardExclusive)
	resp, err := r.call(req)
	if err == nil {
		err = cardErr(l, resp)
	}
	if err != nil {
		r.close()
		return nil, err
	}
	return &cardLink{r: r, l: l, name: name}, nil
}

type cardLink struct {
	r    *rpc
	l    Layout
	name string
	mu   sync.Mutex
}

// Transmit は TRANSMIT。応答は SW1 SW2 込み。
func (c *cardLink) Transmit(apdu []byte) ([]byte, error) {
	req := c.l.cardReq(cardTransmit)
	c.l.setReaderName(req, c.name)
	if err := c.l.setAPDU(req, apdu); err != nil {
		return nil, err
	}
	resp, err := c.r.call(req)
	if err != nil {
		return nil, err
	}
	if err := cardErr(c.l, resp); err != nil {
		return nil, err
	}
	return c.l.cardData(resp)
}

func (c *cardLink) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.r != nil {
		req := c.l.cardReq(cardDisconnect)
		c.l.setReaderName(req, c.name)
		c.r.call(req)
		c.r.close()
		c.r = nil
	}
	return nil
}
