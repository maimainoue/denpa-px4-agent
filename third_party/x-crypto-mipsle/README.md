# x-crypto-mipsle

nasne (MIPS 74Kc、`linux/mipsle`) の TLS で使う ChaCha20-Poly1305 のアセンブリです。標準の Go の GOROOT (`src/vendor/golang.org/x/crypto`) に当てて使います。当て方は `tools/goroot-patch/` と `scripts/build-nasne.sh`。

## 出どころ

[golang/crypto の PR 294](https://github.com/golang/crypto/pull/294) (`x/crypto/chacha20, x/crypto/poly1305: Add MIPSLE assembly version`、作者 stffabi) の、コミット **`2de561af3f503613e18d4cfdfc2b17b07d79dff3`** の 4 ファイルを、**そのままのバイト列**で取り込んだものです。変更はしていません (`SHA256SUMS` で確かめます)。

| ファイル | 上流でのパス |
|---|---|
| `chacha20/chacha_mipsle.go`・`chacha_mipsle.s` | `chacha20/` |
| `internal/poly1305/sum_mipsle.go`・`sum_mipsle.s` | `internal/poly1305/` |

- PR 294 は、2026-11 の時点で**まだマージされていない** (先頭は 2024-05 から変わらない)。上流の PR が変わったり、消えたりしても、この固定のコピーで再現できるように、取り込んでいる
- 上流にマージされたら、この取り込みと、`tools/goroot-patch/` の当てる処理は、要らなくなる (Go の標準のビルドで、MIPSLE のアセンブリが使われる)
- 検証した Go のバージョンは `GO_VERSION` (`go1.27.0`)。ほかのバージョンでは、`tools/goroot-patch/apply.sh` が、ビルドタグの想定の違いで止まる

## 動く機材

- アセンブリは MIPS32 の基本命令だけで動く。`-asmflags=all=-D=GOMIPS_r2` を渡すと、`ROTR` (MIPS32r2 の命令) を使って速くなる
- **`GOMIPS_r2` 付きのバイナリは、MIPS32r2 以上の機材だけで動く** (nasne の 74Kc は MIPS32r2)。r1 の機材で動かすと、不正な命令になる。`scripts/build-nasne.sh` は、`GOMIPS_r2` 付きで作る

## 測定 (nasne 実機、Go 1.27.0)

- AEAD の `Seal` 16 KiB: 標準 約 27 → `ROTR` 付き 約 115〜134 Mbps/CPU 秒 (約 4.5 倍)
- 実放送 (約 16.5 Mbps) の TLS で、エージェントの CPU が、1 コアの 88〜92% → 42〜43%
- 詳しくは `tools/aeadbench/`・`tools/goroot-patch/README.md`・イシュー #3

## ライセンス

| ファイル | ライセンス |
|---|---|
| `*.go` (Go との接着) | BSD 3-Clause (The Go Authors)。`LICENSE.BSD` と `PATENTS` |
| `*.s` (アセンブリ) | Linux カーネルと WireGuard の MIPS 実装 (René van Dorst・Jason A. Donenfeld) の移植で、元は **`GPL-2.0 OR MIT`**。このリポジトリは GPL-2.0 だが、**MIT を選んで取り込む**。`LICENSE.MIT` |

**このバイナリ (`scripts/build-nasne.sh` で作ったもの) を配るときは、`LICENSE.MIT` と `LICENSE.BSD` の内容と著作権表示を付ける**。`scripts/build-nasne.sh` は、バイナリのとなりに `*.licenses.txt` として、両方を書き出す。

## 更新するとき

1. 上流の PR (またはマージ後の `golang.org/x/crypto`) の 4 ファイルに差し替える
2. `SHA256SUMS` を作り直す (`find chacha20 internal -type f | sort | xargs sha256sum > SHA256SUMS`)
3. 上のコミット・`GO_VERSION` を書き換える
4. `scripts/build-nasne.sh` で作って、`tools/aeadbench` で、暗号文の一致 (`ctsum`) と速さを確かめる
