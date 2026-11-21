# goroot-patch

nasne (MIPS 74Kc) のエージェントで、TLS の ChaCha20-Poly1305 を速くするためのツールです ([イシュー #3](https://github.com/maimainoue/denpa-px4-agent/issues/3))。

Go の `crypto/tls` は、GOROOT の中の `src/vendor/golang.org/x/crypto` にある実装を使います。アプリ側の `go.mod` の `replace` では差し替えられないので、**GOROOT を複製して、複製側だけに** golang/crypto の [PR 294](https://github.com/golang/crypto/pull/294) (MIPSLE の ChaCha20・Poly1305 のアセンブリ) を当てます。元の GOROOT は触りません。

**状態: nasne 向けのリリースは、このツールで当てたアセンブリ入りのビルドにする (方針は[イシュー #3](https://github.com/maimainoue/denpa-px4-agent/issues/3) で決めた)。** リリースの手順は `scripts/build-nasne.sh` (取り込み済みの固定のコピー `third_party/x-crypto-mipsle` を、`apply.sh` で当てる)。当てた GOROOT の維持 (Go の更新のたびに当て直し、セキュリティ修正の追従) が要ります。このディレクトリの手順は、PR 294 の最新を試すとき (2 番目の引数に、PR の作業ツリーを渡す) の、開発用です。

## 使い方

```sh
# 1. PR 294 の作業ツリーを用意する
git clone https://go.googlesource.com/crypto /work/xcrypto
git -C /work/xcrypto fetch https://github.com/golang/crypto pull/294/head:pr294
git -C /work/xcrypto worktree add ../xcrypto-pr294 pr294

# 2. GOROOT を複製してパッチを当てる (約 230 MB。Windows の Git Bash で 2 分前後)
tools/goroot-patch/apply.sh /work/goroot-pr294 /work/xcrypto-pr294            # 元の GOROOT は `go env GOROOT`
tools/goroot-patch/apply.sh /work/goroot-pr294 /work/xcrypto-pr294 /usr/local/go   # 指定もできる

# 3. その GOROOT でビルドする
export GOROOT=/work/goroot-pr294 PATH=/work/goroot-pr294/bin:$PATH
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \
  go build -trimpath -tags nasne,nethttpomithttp2 -asmflags='all=-D=GOMIPS_r2' -ldflags=-w -o denpa-px4-agent ./cmd/denpa-px4-agent

# 4. アセンブリがリンクされたか (-s を付けるとシンボルが消えるので、確認のときは -w だけ)
go tool nm denpa-px4-agent | grep -E 'chacha20.xorKeyStream$|poly1305.update$'
```

`apply.sh` がすること (Go 1.27.0 で確認):

- PR のファイル (`chacha20/chacha_mipsle.{go,s}`、`internal/poly1305/sum_mipsle.{go,s}`) を複製側の `src/vendor/golang.org/x/crypto` に足す
- `chacha20/chacha_noasm.go` と `internal/poly1305/mac_noasm.go` のビルドタグに `&& !mipsle` を足す (汎用実装とアセンブリが重複しないように)
- ビルドタグが想定と違う (Go のバージョンが違う) と、止まる

## `-D=GOMIPS_r2` について

- `GOMIPS` の標準の値は `softfloat` と `hardfloat` だけで、`GOMIPS_r2` は定義されません。そのままだと、アセンブリは `ROTR` を使わず `SLL`/`SRL`/`OR` で回転します (MIPS32 の基本命令だけなので、他の `mipsle` 機でも動くはず)
- `-asmflags='all=-D=GOMIPS_r2'` を渡すと `ROTR` を使います。**MIPS32r2 以上の機材だけ**に使ってください (nasne の 74Kc は r2)

## 測定結果 (2026-11-20、nasne 実機、Go 1.27.0)

| 測り方 | 標準 (純 Go) | PR 294 | PR 294 + `ROTR` |
|---|---|---|---|
| AEAD の `Seal` 16 KiB (Mbps/CPU 秒、`tools/aeadbench`) | 約 27 | 約 90 | 約 115〜134 |
| TLS のループバック、`Write` 16 KiB (Mbps/CPU 秒、送受信の合計) | 約 12.7 | 約 32〜35 | 約 48 |
| 実放送 (ch=21、約 16.5 Mbps) でエージェントの CPU (1 コア比) | 88〜92% | (未測定) | 42〜43% |
| 同じ放送でコア全体の使用率 | 94〜97% | (未測定) | 50〜52% |

- x/crypto のテスト (RFC 8439 のベクタ、汎用実装との乱数照合、非アライン、バッファの重なり、カウンターの境界) は、実機で PASS。`TestBurnin` は SKIP (環境変数が要る長時間テスト)
- 暗号文は `tools/aeadbench` の固定の鍵と nonce で、長さ 0〜300 バイトについて一致 (`ctsum=07c52f1f`)

## ライセンス

PR のアセンブリは、Linux カーネルと WireGuard の MIPS 実装 (René van Dorst と Jason A. Donenfeld) の移植で、元は `GPL-2.0 OR MIT` です。ファイルには SPDX と著作権表示だけがあり、許諾文は付いていません。このリポジトリは GPL-2.0 なので MIT を選んで取り込めますが、**このバイナリを再配布するときは、MIT の許諾文と著作権表示を付けてください** (Go のつなぎは BSD)。
