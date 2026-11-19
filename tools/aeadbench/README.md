# aeadbench

nasne (MIPS 74Kc) などで、ChaCha20-Poly1305 の AEAD (`Seal`・`Open`) の速さと、暗号文の一致を測る道具です ([イシュー #3](https://github.com/maimainoue/denpa-px4-agent/issues/3))。

ルートのモジュールとは別のモジュールで、`go test ./...` には入りません。

## 使い方

現行版 (純 Go) と、比べたい実装 (例: [golang/crypto PR 294](https://github.com/golang/crypto/pull/294) の MIPSLE アセンブリ) を、別々の `x/crypto` の作業ツリーに置いてビルドします。

```sh
git clone https://go.googlesource.com/crypto /work/xcrypto-base
git -C /work/xcrypto-base fetch -q https://github.com/golang/crypto pull/294/head:pr294
git -C /work/xcrypto-base worktree add ../xcrypto-pr294 pr294

cp -r tools/aeadbench /work/aead-base && cp -r tools/aeadbench /work/aead-pr
(cd /work/aead-base && go mod edit -require=golang.org/x/crypto@v0.0.0 -replace=golang.org/x/crypto=/work/xcrypto-base && go mod tidy)
(cd /work/aead-pr   && go mod edit -require=golang.org/x/crypto@v0.0.0 -replace=golang.org/x/crypto=/work/xcrypto-pr294 && go mod tidy)

export GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0
(cd /work/aead-base && go build -o aead-base .)
(cd /work/aead-pr   && go build -o aead-pr .)
```

nasne に送って動かします (`/tmp` に置き、終わったら消す)。

```sh
scp /work/aead-base/aead-base /work/aead-pr/aead-pr nasne:/tmp/
ssh nasne 'chmod +x /tmp/aead-base /tmp/aead-pr; /tmp/aead-base; /tmp/aead-pr; rm -f /tmp/aead-base /tmp/aead-pr'
```

## 見るところ

- 1 行目の `ctsum=` が 2 つの実装で同じこと (暗号文が一致する。長さ 0〜300 の範囲)。
- `Seal`・`Open` の Mbps/CPU 秒 (CPU 時間で割るので、ほかのプロセスの影響が小さい)。

## 結果 (2026-11-20、nasne 実機、Go 1.27.0、`linux/mipsle` `softfloat`)

| サイズ | 現行版 (純 Go) | PR 294 | 倍率 |
|---|---|---|---|
| Seal 1316 B | 25.2〜26.2 Mbps/CPU 秒 | 68.5〜78.0 | 約 3.0 倍 |
| Open 1316 B | 25.4〜25.8 | 76.3〜79.9 | 約 3.0 倍 |
| Seal 16 KiB | 26.6〜28.7 | 88.3〜90.7 | 約 3.2 倍 |
| Open 16 KiB | 24.7〜28.4 | 83.9〜93.2 | 約 3.2 倍 |

`ctsum=07c52f1f` で一致 (長さ 0〜300 のみ)。`GOMIPS_r2` が定義されないので `ROTR` は使われず、`SLL`/`SRL`/`OR` の経路。
未確認: 大きいサイズ・非アライン・認証の失敗・RFC 8439 のテストベクタ・`crypto/tls` に差し込んだ全体の負荷。
