package tlsx

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pairRequest は fp で名乗り、その証明書で繋いできた POST /denpa/pair。
func pairRequest(t *testing.T, id *Identity, cert tls.Certificate, fp string) *httptest.ResponseRecorder {
	t.Helper()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	body := `{"client":"` + fp + `","proof":"` + id.proof("client", fp) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/denpa/pair", strings.NewReader(body))
	r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	w := httptest.NewRecorder()
	id.HandlePair(w, r)
	return w
}

func newClientCert(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	dir := t.TempDir()
	if err := create(dir+"/k.key", dir+"/k.crt"); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(dir+"/k.crt", dir+"/k.key")
	if err != nil {
		t.Fatal(err)
	}
	return cert, Fingerprint(cert.Certificate[0])
}

func TestHandlePair_組んだときだけログに出し_2台目には注意を足す(t *testing.T) {
	var logs []string
	id, err := Load(t.TempDir(), strings.Repeat("t", 40), func(f string, a ...any) {
		logs = append(logs, strings.TrimSpace(fmt.Sprintf(f, a...)))
	})
	if err != nil {
		t.Fatal(err)
	}

	c1, fp1 := newClientCert(t)
	if w := pairRequest(t, id, c1, fp1); w.Code != 200 {
		t.Fatalf("1 台目 = %d %s", w.Code, w.Body)
	}
	if len(logs) != 1 || logs[0] != "denpa ("+fp1[:16]+") と組みました" {
		t.Fatalf("logs = %q", logs)
	}

	// 組み直しはログに出さない
	if w := pairRequest(t, id, c1, fp1); w.Code != 200 {
		t.Fatalf("組み直し = %d", w.Code)
	}
	if len(logs) != 1 {
		t.Errorf("組み直しでログが増えた: %q", logs)
	}

	// 2 台目は断らず、注意をログに出す
	c2, fp2 := newClientCert(t)
	if w := pairRequest(t, id, c2, fp2); w.Code != 200 {
		t.Fatalf("2 台目 = %d", w.Code)
	}
	if len(logs) != 3 || !strings.Contains(logs[1], fp2[:16]) || !strings.Contains(logs[2], "ほかの denpa とも組んでいます (2 台)") {
		t.Errorf("logs = %q", logs)
	}
}

func TestRejectUnpaired_組む方法を案内する(t *testing.T) {
	w := httptest.NewRecorder()
	RejectUnpaired(w)
	if w.Code != 403 || !strings.Contains(w.Body.String(), "#pair=") || !strings.Contains(w.Body.String(), `"paired":false`) {
		t.Errorf("%d %s", w.Code, w.Body)
	}
}
