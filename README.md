# denpa-px4-agent

[denpa](https://github.com/danything/denpa) のチューナーエージェントと同じ API を持ち、[px4_drv](https://github.com/tsukumijima/px4_drv) および、recpt1 互換の CLIで選局するエージェントです。

- Linux: px4_drv のデバイスファイル (`/dev/px4video*` など) を ioctl と `read()` で使う。B-CAS は pcscd 経由 (px4-drv-pcsc の内蔵リーダーと外付けリーダー)
- Windows・macOS: px4_drv の DriverHost_PX4 のクライアントになる (BonDriver_PX4・px4rec と同じパイプ)。内蔵リーダーは DriverHost_PX4 のカードのパイプ、外付けリーダーは OS の PC/SC
- 対象の機材: [tsukumijima/px4_drv](https://github.com/tsukumijima/px4_drv) が対応する機種です。
  - PLEX: PX-W3U4・PX-Q3U4・PX-W3PE4・PX-Q3PE4・PX-W3PE5・PX-Q3PE5・PX-MLT5PE・PX-MLT8PE・PX-M1UR・PX-S1UR
  - e-Better: DTV02-1T1S-U (実験的)・DTV02A-1T1S-U・DTV02A-4TS-P・DTV02A-5TS-P・DTV03A-1TU (実験的)
  - 機種の定義は `devices.json` で足したり、受けられる種別を変えたりできます (対象外の機種の動作は保証しません)
- recpt1 互換の CLI (recpt1・recisdb・px4rec) も、`devices.json` にコマンドを書けばチューナーとして呼べる (Linux・macOS)。C/N は出ず、衛星の TSID 指定 (`BS15:16625`) は渡せないので、衛星は番組表の入口しか受けられない
- Sony nasne ([SonyNasne-debian](https://github.com/mar1mo-41414/SonyNasne-debian) の `nasne-recpt1`) も使える。別の機械から HTTP で呼ぶ置き方 (勧める) と、nasne の上で動かす置き方 (mipsel、公式の 2.6.29 カーネルで、起動・API・コマンドの起動までを実機で確認) がある。nasne は 1 回に 1 局しか出さず復号も機材の中で行うので、denpa 側に録画の選局へ局を付ける直しが要る。nasne に B-CAS が無くても、`nasne-recpt1` の `--no-bcas` (生の TS。[maimainoue/SonyNasne-debian](https://github.com/maimainoue/SonyNasne-debian) の fork にしかない) を組み込みの機種 `Sony nasne (nasne-recpt1 HTTP raw)` で使い、復号はエージェントのカードで行える ([仕様書 6.5](docs/spec.md))

denpa 付属のエージェントは PLEX・e-Better の IT930x の機材を USB で直に持つので、px4_drv を入れたホストでは使えません。
このエージェントは px4_drv を残したまま、その機材を denpa で使うためのものです。

- 仕様: [docs/spec.md](docs/spec.md)
- 言語: Go
- ライセンス: [GPL-2.0](LICENSE)

## 実装の状態

仕様書 [19](docs/spec.md) の段階 1〜5 を書きました。

実機で確かめたのは、**Windows (DriverHost_PX4 経由)** と、**Linux (QEMU の VM。px4_drv の chardev と pcscd 経由の B-CAS)** です。どちらも、上の対象には入っていない機種で確かめました。**対象の機種 (PX-W3U4 など) の実機は、まだ確かめていません。** UHF の選局、再選局の繰り返し、内蔵カードリーダーの B-CAS での復号 (NHK 総合の映像と音声の PES の先頭が全部復号済み) まで通りました。ほかの機種・OS (macOS、Linux のそのほかの機種) は、単体テストだけです。この確認で見つかった不具合 (TS のパイプの繋ぎ直し・同期 I/O・受信の止め方) は仕様書 5.4 に反映しています。

| 段階 | 中身 | 場所 |
|---|---|---|
| 1 骨組み | 環境変数・設定ファイル・機材の定義・記録・TLS と組む手順・API 全部・SSE・取り合い・`cmd` バックエンド | `internal/config`・`store`・`devices`・`logx`・`tlsx`・`api`・`manager`・`backend/cmdbackend`・`app` |
| 2 Linux の chardev | 機材の検出 (sysfs)・ioctl・`read()`・C/N・抜き差し | `internal/chardev` |
| 3 カードと復号 | MULTI2・PAT/PMT/CAT/ECM/EMM・カードの管理・`/denpa/card`・`/denpa/decode` | `internal/b25`・`card`・`pcsc` |
| 4 鍵の共有 | 鍵を配る側 (CardWire 互換の `/denpa/card/init`・`ecm`・`confirm`・`emm`)・貰う側 (`CARD_URL`)・LAN の制限・TTL・往復時間・同時の要求の数 | `internal/cardshare` |
| 5 host | DriverHost_PX4 のクライアント (制御・TS・カードのパイプ)。Windows の名前付きパイプと macOS の UNIX ドメインソケット | `internal/host` |

```sh
go test ./...
go build ./cmd/denpa-px4-agent
```

### nasne の上で動かす

公式カーネル (2.6.29) の nasne で、エージェントの起動・API・コマンドの起動まで通ることを実機で確かめました (`accept4` が無いので、代替の待ち受けを内蔵)。地デジの実放送は、TLS で約 16.5 Mbps を 30 秒流せることまで確かめました。denpa 本体から録画・視聴を最後まで通すこと、長時間、衛星は未確認です。ビルドは次のとおりです。

```sh
# 標準のビルド (TLS の暗号は純 Go で、遅い)
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0   go build -trimpath -tags nasne,nethttpomithttp2 -ldflags "-s -w" -o denpa-px4-agent ./cmd/denpa-px4-agent

# アセンブリ入りのビルド (nasne 向けのリリースはこちら。MIPS32r2 以上の機材だけで動く。nasne の 74Kc は r2)
VERSION=v0.1.0 bash scripts/build-nasne.sh
```

実機で測った負荷と、メモリ・サイズの結果は仕様書 6.5 の置き方 B にあります (メモリは足りる。実放送の約 16.5 Mbps を TLS で流すと、エージェントは 1 コアの約 90%、コア全体は 94〜97% で、余裕がほとんど無い。大半は TLS の暗号 (純 Go の ChaCha20-Poly1305) です。さらに denpa は、視聴中に同じ TS をロゴの取得でもう 1 本読み続けるので、実際には 2 本ぶん要ります)。余裕を取るなら、エージェントを別の機械に置く置き方 A を使ってください。暗号の MIPS アセンブリ入りのビルド (`scripts/build-nasne.sh`。エージェントの CPU が約半分になり、denpa の視聴 (2 本) でも 1 コアに収まる) が、nasne 向けのリリースになります ([#3](https://github.com/maimainoue/denpa-px4-agent/issues/3))。アセンブリは golang/crypto の PR 294 の固定のコピー (`third_party/x-crypto-mipsle`) で、**配るときは、バイナリのとなりの `*.licenses.txt` (MIT と BSD のライセンス) を付けてください**。

### Docker とリリース

タグ `v*` を push すると、GitHub Actions が linux/amd64 と linux/arm64 をビルドして、GHCR に `ghcr.io/maimainoue/denpa-px4-agent:latest` などを公開します。Actions から手動で実行したときは、`dev-<短い SHA>` のタグで出します。nasne (HTTP) の組み込みは、`curl` を使わずエージェントが HTTP で直接読むので、Docker のイメージでも使えます ([#17](https://github.com/maimainoue/denpa-px4-agent/issues/17))。

手元で動かすときは、ホストの単一のプラットフォームにして、`--load` を足します。

```sh
docker buildx build --platform linux/amd64 --load \
  --build-arg VERSION=v0.1.0 -t denpa-px4-agent:local .
```

手元で公開するときは、複数のプラットフォームにして `--push` を使います (ふだんは、タグの push でワークフローが公開するので、手元で公開するときの書き方です)。

```sh
docker buildx build --platform linux/amd64,linux/arm64 --push \
  --build-arg VERSION=v0.1.0 -t ghcr.io/maimainoue/denpa-px4-agent:latest .
```

リリース用のバイナリ (linux・windows・macOS・nasne 向け mipsle) は、スクリプトで作ります。`dist/` に出ます。nasne 向け (mipsle) は、ChaCha20-Poly1305 のアセンブリ入りの専用ビルド (`scripts/build-nasne.sh`) で、専用に複製した GOROOT を使うので、ほかより時間がかかります。**MIPS32r2 以上の機材だけで動き** (nasne の 74Kc は r2)、バイナリのとなりに `denpa-px4-agent-linux-mipsle.licenses.txt` (アセンブリの MIT と、Go の BSD のライセンス) もできます。**配るときは、一緒に付けてください。**

```sh
VERSION=v0.1.0 bash scripts/build-release.sh
```

denpa と一緒に使うときは、`deploy/compose.override.yml` を denpa の `compose.yml` と同じフォルダに置きます。

### 鍵を配る・貰う (カード共有)

カードを挿した機械が、同じ LAN の、カードを持たない機械に、鍵を配れます (denpa の付属のエージェントと同じ CardWire 互換の API。仕様書 [9.5](docs/spec.md))。nasne のようにカードリーダーが無い機械や、`nasne-recpt1 --no-bcas` の生の TS ([仕様書 6.5](docs/spec.md)) を、別の機械のカードで復号するときに使います。

| 環境変数 | 意味 |
|---|---|
| `CARD_SHARE_PORT` | 書くと、そのポートで鍵を配る (カードのある側)。書かなければ配らない。`AGENT_PORT` と同じ値は起動を止める |
| `CARD_URL` | `http://ホスト:ポート` の形で、鍵を配る側 (`CARD_SHARE_PORT` のポート) を書くと、手元のカードではなく、そこから鍵を貰う (カードのない側) |
| `CARD_SHARE_NETWORKS` | 配る相手のアドレスの範囲 (カンマ区切り)。既定はプライベートアドレスだけ (`127.0.0.0/8`・`10.0.0.0/8`・`172.16.0.0/12`・`192.168.0.0/16` と IPv6 のループバック・ULA・リンクローカル)。既定の範囲の外 (グローバルなど) を書くと起動しない |
| `CARD_TTL` | 配るとき・貰うときの TTL (IPv6 は hop limit)。既定 1 (ルーターを越えない)、1〜8 |
| `CARD_SHARE_MAX_RTT` | 往復時間 (ミリ秒) がこれを超える相手には配らない。既定は見ない (0) |

- **平文の HTTP で鍵を配るので、信頼できる LAN の中だけで使ってください。** 範囲・TTL・往復時間・同時の要求の数 (4 まで。超えたら 503) で絞りますが、暗号化も認証もありません
- `CARD_URL` と `CARD_SHARE_PORT` を両方書くと、配るのは手元のカードだけです (貰った鍵はさらに配らない)
- 実機で、A-CAS と B-CAS のカードを別の機械から貰い、復号した結果が、手元のカードで復号したものとバイト単位で一致することを確かめました。受け入れられる EMM (契約の更新) の流れは、実機では未確認です

### 未実装・未確認

- A-CAS カードは B-CAS 互換モードで使えます (B-CAS の INT が断られたら P2 を変えて開き直す)。実機の A-CAS カード (外付けの SCR3310) と、ケーブル回線で、MPEG-2 TS のスクランブルを復号できることを確かめました。契約確認 (`0x3C`) の P2 は未確認です。4K の MMT/TLV (ARIB STD-B61) は、A-CAS のカードで復号します ([docs/b61.md](docs/b61.md)。実機のケーブル回線で、復号の結果の形まで確かめました。映像として再生できるかは denpa 側で確認が要ります)
- Linux の B-CAS は pcscd 経由 (`internal/pcsc`。pure Go で pcscd の socket に直接つなぐ) で使えます。内蔵リーダー (px4-drv-pcsc) は実機の pcscd 1.9.9 と B-CAS カードで確かめました。外付けリーダー (libccid) は pcscd から同じように見えるはずですが、実物では確かめていません。Windows の外付けリーダーは、OS の PC/SC (System32 の `winscard.dll`。`internal/winscard`) で使えます。`WINSCARD_DLLS` に絶対パスを書くと、別の場所の PC/SC の DLL も足せます (`;` 区切り)。実機の Windows で DLL を読んでリーダーを引くところまでは確かめました (リーダー 0 個)。Windows の CI は、Smart Card サービスが無く `SCARD_E_NO_SERVICE` になっても試験を成功にするので、一覧を引けたかは試験のログで確かめてください。外付けリーダーとカードでの復号は、まだ確かめていません。macOS の外付けリーダーは、PCSC.framework で使えます (同じ `internal/winscard`。cgo は使わず `purego` で呼びます)。macOS の CI では、PCSC.framework を読んでリーダーの一覧を引くところまでを試していて、CI のログ (`go test -v ./internal/winscard`) では、リーダー 0 個の一覧を引けました。ただし、PC/SC のサービスに繋がらなくても試験は成功にするので、CI が通っただけでは一覧を引けたとは言えません。一覧を引けたかは、試験のログで確かめてください。外付けリーダーとカードでの復号は、まだ確かめていません。PLEX の内蔵リーダーは PCSC.framework には出ません。Windows と macOS の内蔵リーダーは、DriverHost_PX4 のカードのパイプで使えます
- `--tune`・`--devices`・`--card` はありません (`--decode-file` はあります)
- ISDB-C (`CATV`) と J.83B (`SKY`) は、chardev では `PTX_SET_SYSTEM_MODE(0x40)` + `PTX_SET_CHANNEL` (TSMF の 0 本目) と、`PTXT_SET_PARAMS` + `PTXT_TUNE` (TSMF の 1 本目以降と J.83B) で選局します。ISDB-C の映像と TSMF の 1 本目以降、J.83Bは実際の放送では確かめていません。TSMF の `X-Denpa-Tsmf-Streams` は返しません。
- Windows と macOS の機材の自動検出 (SetupDi・IOKit) は無いので、`tuners.json` に `px4host:<GUID>:<受信機の番号>:<台目>` を手で書きます。組み込みの表で GUID を持つのは PX-W3U4 だけです
- MULTI2 は ARIB STD-B25 の試験ベクタで確かめていません (暗号化と復号の往復だけ)。ioctl の引数の渡し方 (`PTX_SET_SYSTEM_MODE`・`PTX_ENABLE_LNB_POWER` は値渡し) と、ECM の返り値のうち鍵を使ってよいもの (`0x0000`・`0x0200`・`0x0400`・`0x0800`) は実機で確かめます
- 最初の TS が届く前に溜める復号の細かい決まり (`service` を付けた利用者だけの早出し、PMT が来ないときの扱い) は簡略です
- `/denpa/card/confirm` と `/denpa/decode` の `decryptedByDevice` の 500 は返していません
- denpa の適合テスト (`agent/conformance.test.ts`、66 件) を回しました。**Linux (WSL の Ubuntu 24.04) では 66 件全部が通ります。Windows は 62 件が通り、4 件が通りません。** 通らない 4 件は止まれの合図の試験で、Windows の `kill("SIGINT")` がプロセスをそのまま終わらせるための、試験側の差と見ています (同じエージェントが Linux では通ります)。macOS は回していません。適合テストを回すには、denpa のリポジトリで `bun install` し、`AGENT_CMD` にこのエージェントの実行ファイルを渡して `bun test agent/conformance.test.ts` を実行します (`openssl` が PATH に要ります)
