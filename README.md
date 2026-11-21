# denpa-px4-agent

[denpa](https://github.com/danything/denpa) のチューナーエージェントと同じ API を持ち、[px4_drv](https://github.com/tsukumijima/px4_drv) および、recpt1 互換の CLIで選局するエージェントです。

- Linux: px4_drv のデバイスファイル (`/dev/px4video*` など) を ioctl と `read()` で使う。B-CAS は pcscd 経由 (px4-drv-pcsc の内蔵リーダーと外付けリーダー)
- Windows・macOS: px4_drv の DriverHost_PX4 のクライアントになる (BonDriver_PX4・px4rec と同じパイプ)。内蔵リーダーは DriverHost_PX4 のカードのパイプ、外付けリーダーは OS の PC/SC
- 対象の機材: [tsukumijima/px4_drv](https://github.com/tsukumijima/px4_drv) が対応する機種です。
  - PLEX: PX-W3U4・PX-Q3U4・PX-W3PE4・PX-Q3PE4・PX-W3PE5・PX-Q3PE5・PX-MLT5PE・PX-MLT8PE・PX-M1UR・PX-S1UR
  - e-Better: DTV02-1T1S-U (実験的)・DTV02A-1T1S-U・DTV02A-4TS-P・DTV02A-5TS-P・DTV03A-1TU (実験的)
  - 機種の定義は `devices.json` で足したり、受けられる種別を変えたりできます (対象外の機種の動作は保証しません)
- recpt1 互換の CLI (recpt1・recisdb・px4rec) も、`devices.json` にコマンドを書けばチューナーとして呼べる (Linux・macOS)。C/N は出ず、衛星の TSID 指定 (`BS15:16625`) は渡せないので、衛星は番組表の入口しか受けられない
- Sony nasne ([SonyNasne-debian](https://github.com/mar1mo-41414/SonyNasne-debian) の `nasne-recpt1`) も使える。別の機械から HTTP で呼ぶ置き方 (勧める) と、nasne の上で動かす置き方 (mipsel、公式の 2.6.29 カーネルで、起動・API・コマンドの起動までを実機で確認) がある。nasne は 1 回に 1 局しか出さず復号も機材の中で行うので、denpa 側に録画の選局へ局を付ける直しが要る

denpa 付属のエージェントは PLEX・e-Better の IT930x の機材を USB で直に持つので、px4_drv を入れたホストでは使えません。
このエージェントは px4_drv を残したまま、その機材を denpa で使うためのものです。

- 仕様: [docs/spec.md](docs/spec.md)
- 言語: Go
- ライセンス: [GPL-2.0](LICENSE)

## 実装の状態

仕様書 [19](docs/spec.md) の段階 1・2・3・5 を書きました。

実機で確かめたのは、**Windows (DriverHost_PX4 経由)** と、**Linux (QEMU の VM。px4_drv の chardev と pcscd 経由の B-CAS)** です。どちらも、上の対象には入っていない機種で確かめました。**対象の機種 (PX-W3U4 など) の実機は、まだ確かめていません。** UHF の選局、再選局の繰り返し、内蔵カードリーダーの B-CAS での復号 (NHK 総合の映像と音声の PES の先頭が全部復号済み) まで通りました。ほかの機種・OS (macOS、Linux のそのほかの機種) は、単体テストだけです。この確認で見つかった不具合 (TS のパイプの繋ぎ直し・同期 I/O・受信の止め方) は仕様書 5.4 に反映しています。

| 段階 | 中身 | 場所 |
|---|---|---|
| 1 骨組み | 環境変数・設定ファイル・機材の定義・記録・TLS と組む手順・API 全部・SSE・取り合い・`cmd` バックエンド | `internal/config`・`store`・`devices`・`logx`・`tlsx`・`api`・`manager`・`backend/cmdbackend`・`app` |
| 2 Linux の chardev | 機材の検出 (sysfs)・ioctl・`read()`・C/N・抜き差し | `internal/chardev` |
| 3 カードと復号 | MULTI2・PAT/PMT/CAT/ECM/EMM・カードの管理・`/denpa/card`・`/denpa/decode` | `internal/b25`・`card`・`pcsc` |
| 5 host | DriverHost_PX4 のクライアント (制御・TS・カードのパイプ)。Windows の名前付きパイプと macOS の UNIX ドメインソケット | `internal/host` |

```sh
go test ./...
go build ./cmd/denpa-px4-agent
```

### nasne の上で動かす

公式カーネル (2.6.29) の nasne で、エージェントの起動・API・コマンドの起動まで通ることを実機で確かめました (`accept4` が無いので、代替の待ち受けを内蔵)。地デジの実放送は、TLS で約 16.5 Mbps を 30 秒流せることまで確かめました。denpa 本体から録画・視聴を最後まで通すこと、長時間、衛星は未確認です。ビルドは次のとおりです。

```sh
GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0   go build -trimpath -tags nasne,nethttpomithttp2 -ldflags "-s -w" -o denpa-px4-agent ./cmd/denpa-px4-agent
```

実機で測った負荷と、メモリ・サイズの結果は仕様書 6.5 の置き方 B にあります (メモリは足りる。実放送の約 16.5 Mbps を TLS で流すと、エージェントは 1 コアの約 90%、コア全体は 94〜97% で、余裕がほとんど無い。大半は TLS の暗号 (純 Go の ChaCha20-Poly1305) です。さらに denpa は、視聴中に同じ TS をロゴの取得でもう 1 本読み続けるので、実際には 2 本ぶん要ります)。余裕を取るなら、エージェントを別の機械に置く置き方 A を使ってください。暗号の MIPS アセンブリ化 (エージェントの CPU が約半分になり、denpa の視聴 (2 本) でも 1 コアに収まった) は検討中です ([#3](https://github.com/maimainoue/denpa-px4-agent/issues/3))。

### 未実装・未確認

- A-CAS カードは B-CAS 互換モードで使えます (B-CAS の INT が断られたら P2 を変えて開き直す)。実機の A-CAS カード (外付けの SCR3310) と、ケーブル回線で、MPEG-2 TS のスクランブルを復号できることを確かめました。契約確認 (`0x3C`) の P2 は未確認です。4K の MMT/TLV (ARIB STD-B61) は、A-CAS のカードで復号します ([docs/b61.md](docs/b61.md)。実機のケーブル回線で、復号の結果の形まで確かめました。映像として再生できるかは denpa 側で確認が要ります)
- Linux の B-CAS は pcscd 経由 (`internal/pcsc`。pure Go で pcscd の socket に直接つなぐ) で使えます。内蔵リーダー (px4-drv-pcsc) は実機の pcscd 1.9.9 と B-CAS カードで確かめました。外付けリーダー (libccid) は pcscd から同じように見えるはずですが、実物では確かめていません。OS の PC/SC (Windows の `winscard.dll`、macOS の PCSC.framework) の外付けリーダーはまだありません。Windows と macOS の内蔵リーダーは、DriverHost_PX4 のカードのパイプで使えます
- 鍵を配る・貰う (`CARD_SHARE_PORT`・`CARD_URL`。段階 4) と、`--tune`・`--devices`・`--card`・`--decode-file`・`--status` はありません
- ISDB-C (`CATV`) と J.83B (`SKY`) は、chardev では `PTX_SET_SYSTEM_MODE(0x40)` + `PTX_SET_CHANNEL` (TSMF の 0 本目) と、`PTXT_SET_PARAMS` + `PTXT_TUNE` (TSMF の 1 本目以降と J.83B) で選局します。ISDB-C の映像と TSMF の 1 本目以降、J.83Bは実際の放送では確かめていません。TSMF の `X-Denpa-Tsmf-Streams` は返しません。`SKY` の周波数をずらすチャンネルの再試行 (仕様書 7.2) はまだありません
- Windows と macOS の機材の自動検出 (SetupDi・IOKit) は無いので、`tuners.json` に `px4host:<GUID>:<受信機の番号>:<台目>` を手で書きます。組み込みの表で GUID を持つのは PX-W3U4 だけです
- MULTI2 は ARIB STD-B25 の試験ベクタで確かめていません (暗号化と復号の往復だけ)。ioctl の引数の渡し方 (`PTX_SET_SYSTEM_MODE`・`PTX_ENABLE_LNB_POWER` は値渡し) と、ECM の返り値のうち鍵を使ってよいもの (`0x0000`・`0x0200`・`0x0400`・`0x0800`) は実機で確かめます
- 最初の TS が届く前に溜める復号の細かい決まり (`service` を付けた利用者だけの早出し、PMT が来ないときの扱い) は簡略です
- `/denpa/card/confirm` と `/denpa/decode` の `decryptedByDevice` の 500 は返していません
- denpa の適合テスト (`conformance.test.ts`) はまだ回していません
