# denpa-px4-agent

[denpa](https://github.com/danything/denpa) のチューナーエージェントと同じ API を持ち、[px4_drv](https://github.com/tsukumijima/px4_drv) の機材で選局するエージェントです。

- Linux: px4_drv のデバイスファイル (`/dev/px4video*` など) を ioctl と `read()` で使う。B-CAS は pcscd 経由 (px4-drv-pcsc の内蔵リーダーと外付けリーダー)
- Windows・macOS: px4_drv の DriverHost_PX4 のクライアントになる (BonDriver_PX4・px4rec と同じパイプ)。内蔵リーダーは DriverHost_PX4 のカードのパイプ、外付けリーダーは OS の PC/SC
- 機材: PLEX・e-Better の IT930x の機材、TurboSight TBS 5530 (地上波とケーブル)、MyGica T230C、Sony CECH-ZD1 J、PIXELA PIX-DT295、I-O DATA GV-MVP/XZ、Siano RIO 系。機種の定義は `devices.json` で足したり、受けられる種別を変えたりできる
- recpt1 互換の CLI (recpt1・recisdb・px4rec) も、`devices.json` にコマンドを書けばチューナーとして呼べる (Linux・macOS)。C/N は出ず、衛星の TSID 指定 (`BS15:16625`) は渡せないので、衛星は番組表の入口しか受けられない
- Sony nasne ([SonyNasne-debian](https://github.com/mar1mo-41414/SonyNasne-debian) の `nasne-recpt1`) も使える。別の機械から HTTP で呼ぶ置き方 (勧める) と、nasne の上で動かす置き方 (mipsel、6.12 カーネル) がある。nasne は 1 回に 1 局しか出さず復号も機材の中で行うので、denpa 側に録画の選局へ局を付ける直しが要る

denpa 付属のエージェントは PLEX・e-Better の IT930x の機材を USB で直に持つので、px4_drv を入れたホストでは使えません。
このエージェントは px4_drv を残したまま、その機材を denpa で使うためのものです。

- 仕様: [docs/spec.md](docs/spec.md)
- 言語: Go
- ライセンス: [GPL-2.0](LICENSE)

## 実装の状態

仕様書 [19](docs/spec.md) の段階のうち、`cmd` バックエンド ([6](docs/spec.md)) だけ書いてあります。HTTPS の API・取り合い・chardev・host・復号はまだありません。

- `internal/channel`: チャンネル名を読む (7.1)
- `internal/devices`: 機材の定義 (`devices.json`) の読み方と重ね方、`fake` 機種
- `internal/backend`: `Backend`・`Tuner` のインターフェースと選局の失敗
- `internal/backend/cmdbackend`: recpt1 互換の CLI のバックエンド (チャンネル名の直し方、引数の組み立て、子プロセスの起動と停止、`singleService`・`decryptedByDevice`・`busyExitCode`)

```sh
go test ./...
```
