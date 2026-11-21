# denpa-px4-agent 仕様書

denpa のチューナーエージェントと同じ API を持ち、px4_drvの機材で選局し、その内蔵カードリーダーと外付けのカードリーダーで B-CAS を使うエージェントの仕様です。
Linux では px4_drv のデバイスファイル (`/dev/px4video*` など) を、Windows と macOS では px4_drv の DriverHost_PX4 を使います。
コードを書く人向けに、denpa との約束、px4_drv との約束、B-CAS と復号、設定、配布、テストを書きます。
denpa の設計の理由は denpa の `docs/agent.md` にあり、この文書は「何をどう作るか」だけを書きます。

書き方は denpa の `docs/writing.md` に合わせます (画面に出る文言、ログ、コメント、テスト名)。

## 1. 目的と範囲

**denpa から見て、付属のエージェント (`ghcr.io/danything/denpa-agent`) と入れ替えて使えるものを作ります。** 入れ替えても denpa の設定・画面・予約の割り振り・適合テストはそのまま通ります。

- 言語は Go、ライセンスは GPL-2.0
- 対象は Linux (amd64・arm64)、Windows (x64)、macOS (Apple Silicon・Intel) の 3 つ。機材に触る層だけを OS ごとに分け、ほかは共通にする
- 機材は [tsukumijima/px4_drv](https://github.com/tsukumijima/px4_drv) が対応する機種だけ (PLEX の PX-W3U4・PX-Q3U4・PX-W3PE4・PX-Q3PE4・PX-W3PE5・PX-Q3PE5・PX-MLT5PE・PX-MLT8PE・PX-M1UR・PX-S1UR、e-Better の DTV02-1T1S-U・DTV02A-1T1S-U・DTV02A-4TS-P・DTV02A-5TS-P・DTV03A-1TU)。[3.2](#32-組み込みの表)
- **機材の定義は設定ファイルで足せます。** 機種ごとに受けられる種別 (BS を受けられるかなど) も設定で変えられます ([3](#3-機材の定義))
- 選局は、Linux では px4_drv のデバイスファイルの ioctl と `read()`、Windows と macOS では DriverHost_PX4 のパイプで行う。USB は直に触らない
- recpt1 互換の CLI (recpt1・recisdb・px4rec) も、設定ファイルに書けばチューナーとして呼べる ([6](#6-コマンド-recpt1-互換の-cli-のバックエンド))。C/N は出ず、TSID の指定もできない逃げ道
- Sony nasne (SonyNasne-debian の `nasne-recpt1`) も、この CLI の仕組みで使える ([6.5](#65-sony-nasne-sonynasne-debian-の-nasne-recpt1))。別の機械から HTTP で呼ぶ置き方と、nasne の上で動かす置き方 (mipsel のビルド) の両方を書く
- B-CAS は、Linux では pcscd 経由 (px4-drv-pcsc の内蔵リーダーと libccid の外付けリーダー)、Windows と macOS では内蔵リーダーを DriverHost_PX4 のカードのパイプ、外付けリーダーを OS の PC/SC で使う
- スクランブルの復号 (ARIB STD-B25、MULTI2) はエージェントの中で行う
- 付属のエージェントと同じく、denpa とは TLS で組む。鍵を配る API (LAN の中の別の拠点に鍵を配る) も持つ

しないこと。

- Linux DVB・USB を直に持つ機材・asicen-userland・Android
- NIT・SDT・EIT の解釈。番組や局の中身は denpa が読む
- DriverHost_PX4 の中身の変更。DriverHost_PX4 と px4_drv のカーネルモジュールは px4_drv の側で保守する

### 用語

| 言葉           | 指すもの                                                                                                                       |
| -------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| チューナー     | 受信機 1 つ。Linux ではデバイスファイル 1 つ、Windows と macOS では DriverHost_PX4 の受信機 1 つ。`GET /denpa/tuners` の 1 行   |
| 機材           | 筐体 1 つ (USB デバイス 1 つ)。PX-Q3 系は USB デバイスが 2 つなので、同じ筐体に 2 つの機材がある                               |
| 機種           | 機材の種類 (PX-W3U4、PX-MLT5PE など)。[機材の定義](#3-機材の定義) の 1 項目                                                      |
| 選局           | 1 つのチューナーで 1 つの物理チャンネルを受けている状態 (Lease)。利用者を何人でもぶら下げる                                    |
| 利用者         | `GET /denpa/stream` の 1 接続 (Sink)。`use` で名乗る                                                                           |
| 種別           | `GR` (地上波) / `BS` / `CS` / `CATV` (ケーブル。ISDB-C) / `SKY` (スカパー！プレミアムサービス光。J.83 Annex B)                   |
| 方式           | 復調の方式。ISDB-T (`GR`)・ISDB-S (`BS` と `CS`)・ISDB-C (`CATV`)・J.83B (`SKY`)                                                |
| チャンネル名   | `T27`・`C30`・`BS15:16625`・`CS02`・`QC30:2`・`H44` のような物理チャンネルの名前 ([7.1](#71-チャンネル名))                       |
| バックエンド   | 機材に触る層。`chardev` (Linux)、`host` (Windows・macOS の DriverHost_PX4)、`cmd` (recpt1 互換の CLI。Linux・macOS)             |

## 2. 構成

```text
denpa ──(HTTPS、クライアント証明書)──▶ denpa-px4-agent
                                          │
                     ┌────────────────────┼──────────────────────────┐
                     │ Linux              │ Windows                  │ macOS
                     ▼                    ▼                          ▼
              /dev/px4video* など   \\.\pipe\px4_ctrl_pipe      $PX4_RUNTIME_DIR/px4_ctrl_pipe.sock
              (ioctl / read)        \\.\pipe\px4_data_pipe      $PX4_RUNTIME_DIR/px4_data_pipe.sock
                     │              \\.\pipe\px4_card_pipe      $PX4_RUNTIME_DIR/px4_card_pipe.sock
              pcscd (内蔵は            └── DriverHost_PX4.exe     └── DriverHost_PX4 (root で常駐)
              px4-drv-pcsc、          外付けリーダーは             外付けリーダーは
              外付けは libccid)       System32 の winscard.dll     PCSC.framework
```

プロセスは 1 つで、goroutine で分けます。

| 役目         | 中身                                                                                                                 |
| ------------ | -------------------------------------------------------------------------------------------------------------------- |
| API          | HTTPS (25252)。`/denpa/*`。組んだ denpa だけ通す                                                                     |
| 取り合い     | チューナーの選び方・共有・取り上げ・引き継ぎ ([8](#8-取り合い))                                                      |
| 選局         | チューナー 1 つにつき goroutine 1 つ。バックエンドで選局し、受けた TS を復号の列に渡す                               |
| バックエンド | `chardev` ([4](#4-linux-px4_drv-のデバイスファイル)) と `host` ([5](#5-windowsmacos-driverhost_px4-のクライアント))。同じインターフェース (`Tuner`) を実装する |
| 復号         | 選局 1 つにつき 1 つ。PAT・PMT・ECM を読んで MULTI2 で復号し、利用者に配る ([9](#9-b-cas-と復号))                    |
| カード       | ECM・EMM・契約確認。同時には 1 つだけ送る。カードへの経路は OS ごと ([9.1](#91-カードリーダー))                       |
| 鍵を配る     | `CARD_SHARE_PORT` を書いたときだけ。平文の HTTP で LAN の中にだけ                                                    |
| 通知         | SSE (`/denpa/events`)                                                                                                |
| 抜き差し     | 機材の増減を見て、チューナーの一覧を更新する                                                                         |

バックエンドのインターフェース (Go)。取り合いと復号はこれだけを見ます。

```go
type Backend interface {
    // 挿さっている機材を機材の定義と突き合わせて並べる (自動検出)
    Detect(defs *DeviceDefs) ([]TunerSpec, error)
    // device 文字列 (px4:… / px4host:…) からチューナーを開く。開いたままにする
    Open(spec TunerSpec) (Tuner, error)
}

type Tuner interface {
    Tune(t Tuning) error          // 選局する。開いたまま何度でも呼べる。同期しなければ ErrNoLock
    Start() (io.ReadCloser, error) // TS (か TLV) の読み出し。Tune のあとで取る
    Stop() error
    Signal() (*TunerSignal, error) // 受信の状態と C/N。受けていなければ nil
    SetLnb(voltage int) error      // 0 / 11 / 15。対応しない機材は ErrUnsupported
    Close() error
}
```

## 3. 機材の定義

**機種の表は `devices.json` に持ち、エージェントが組み込みの表を持ちつつ、利用者が同じ形式のファイルで足したり上書きしたりできます。** `tuners.json` の隣 (`DEVICES_FILE` で変えられる) に置きます。

- 起動時に組み込みの表を読み、`devices.json` があればその項目を `model` をキーに重ねます (同じ `model` は上書き、無い `model` は追加)
- 1 項目の書き損じはその項目だけ飛ばし、理由を記録に出します
- 変えたら再起動します (動いている間に読み直さない)

### 3.1 項目

```json
{
  "devices": [
    {
      "model": "PLEX PX-W3U4",
      "usb": ["0511:083f"],
      "linux": { "devname": "px4video" },
      "host": { "deviceGuid": "{aa8de3fc-004d-49a4-b976-4250f15a976d}" },
      "receivers": [
        { "index": 0, "types": ["BS", "CS"] },
        { "index": 1, "types": ["BS", "CS"] },
        { "index": 2, "types": ["GR"] },
        { "index": 3, "types": ["GR"] }
      ],
      "lnb": [15],
      "cardReader": true,
      "pairBySerial": false
    }
  ]
}
```

| 項目                 | 意味                                                                                                                                                                              |
| -------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `model`              | 機種名。チューナーの名前の先頭に使う。重ねるときのキー                                                                                                                            |
| `usb`                | USB の ID (`vvvv:pppp`)。Linux では sysfs の `idVendor:idProduct`、macOS では IOKit の照合に使う。複数書ける                                                                       |
| `linux.devname`      | Linux のデバイスファイルの名前の先頭 (`px4video` なら `/dev/px4video<N>`)。sysfs のクラスは `devname` から `video` を除いたもの (`px4`)                                              |
| `host.deviceGuid`    | DriverHost_PX4.ini の `[DeviceDefinitionN]` の `GUID`。Windows と macOS で OPEN の鍵にする。`host.receiverGuids` に受信機ごとの `GUID` を書いてもよい (書かなければ `index` で開く) |
| `receivers`          | 受信機の並び。`index` は筐体の中の番号 (Linux はデバイスファイルの番号の順、host は定義の `Index`)。`types` はその受信機が受けられる種別                                           |
| `receivers[].systems` | 省略可。host の OPEN に渡す方式 (`ISDB-T`・`ISDB-S`・`ISDB-C`・`J.83B`)。無ければ `types` から決める (`GR`→ISDB-T、`BS`/`CS`→ISDB-S、`CATV`→ISDB-C、`SKY`→J.83B)                 |
| `lnb`                | 給電できる電圧の一覧 (`[15]`、`[11, 15]`、`[]`)。無い電圧を設定に書かれたら衛星の選局を断る                                                                                        |
| `cardReader`         | 内蔵カードリーダーがあるか。Linux では pcscd のリーダー名の `[ID: …]`、host ではカードのパイプの `LIST_READERS` に出る                                                            |
| `pairBySerial`       | PX-Q3 系のように、USB のシリアル番号の末尾 (`1`・`2`) で 2 つの USB デバイスを同じ筐体にまとめるか                                                                                   |
| `command` / `channelFormat` / `lnbArg` / `env` | 書くと `cmd` バックエンドの機種になる ([6.1](#61-機材の定義の書き方))。`command` の代わりに `url` を書くと、コマンドを起動せず、エージェントが HTTP の GET で TS を直接読む ([6.5](#65-sony-nasne-sonynasne-debian-の-nasne-recpt1))                                                                                                   |
| `singleService` / `decryptedByDevice` / `serviceArg` / `tsidArg` / `busyExitCode` | 1 サービスしか出ない機材と、機材の中で復号する機材のため ([6.4](#64-1-サービスしか出ない機材と機材の中で復号する機材))                                             |
| `notes`              | 自由な文。画面には出さない                                                                                                                                                         |

**`types` を変えれば、その機種で受ける種別を絞れます。** 衛星のアンテナが無い家で PX-W3U4 の受信機から `BS` `CS` を外す、PX-MLT5PE でケーブルを受けるときに `CATV` を足す、のように使います。`GET /denpa/tuners` の `receivable` はここの `types` をそのまま返し、denpa の設定はこれに無い種別を選ばせません。自動検出した `tuners.json` の `types` には、`GR` `BS` `CS` のうち `receivable` にあるものだけを入れます (`CATV` と `SKY` はスキャンが長くなるので、人が足す)。

### 3.2 組み込みの表

| 機種 (`model`)                     | USB                                      | Linux `devname`  | 受信機と種別                                                         | LNB  | カード | 備考                                                                                   |
| ---------------------------------- | ---------------------------------------- | ---------------- | -------------------------------------------------------------------- | ---- | ------ | -------------------------------------------------------------------------------------- |
| PLEX PX-W3U4 / W3PE4 / W3PE5       | `0511:083f` / `023f` / `073f`            | `px4video`       | 0・1: `BS` `CS`、2・3: `GR`                                           | 15   | あり   |                                                                                        |
| PLEX PX-Q3U4 / Q3PE4 / Q3PE5       | `0511:084a` / `024a` / `074a`            | `px4video`       | 同上 (USB デバイス 2 つ)                                              | 15   | 片側   | `pairBySerial: true`                                                                   |
| PLEX PX-MLT5PE / MLT5U、DTV02A-5TS-P | `0511:024e` / `084e` / `924e`          | `pxmlt5video`    | 0〜4: `GR` `BS` `CS` `CATV` `SKY`                                     | 15   | あり   |                                                                                        |
| PLEX PX-MLT8PE                     | `0511:0253` (5 つ)、`0511:0252` (3 つ)   | `pxmlt8video`    | 同上                                                                  | 15   | 5 つの側 | USB デバイスごとに別の機材                                                            |
| e-Better DTV02A-4TS-P              | `0511:0254`                              | `isdb6014video`  | 0〜3: 同上                                                            | 15   | あり   |                                                                                        |
| DTV02-1T1S-U / DTV02A-1T1S-U       | `0511:004b` / `084b`                     | `isdb2056video`  | 0: `GR` `BS` `CS`                                                     | 無し | あり   | DTV02-1T1S-U は px4_drv で実験的                                                       |
| PLEX PX-M1UR                       | `0511:0854`                              | `pxm1urvideo`    | 0: `GR` `BS` `CS`                                                     | 無し | あり   |                                                                                        |
| PLEX PX-S1UR                       | `0511:0855`                              | `pxs1urvideo`    | 0: `GR`                                                               | 無し | あり   |                                                                                        |
| e-Better DTV03A-1TU                | `0511:0052`                              | `isdbt2071video` | 0: `GR`                                                               | 無し | あり   | px4_drv で実験的 (ロット番号 2021-11 以降の個体だけ)                                    |

この表が、対象の機種のすべてです (tsukumijima/px4_drv の「対応デバイス」と同じ)。

nasne (HTTP) は px4_drv の機種ではなく、`cmd` バックエンドの組み込みです ([6.5](#65-sony-nasne-sonynasne-debian-の-nasne-recpt1))。

`host.deviceGuid` は px4_drv の `DriverHost_PX4.ini` の値を組み込みの表に写します。同じ ini を `DRIVER_HOST_INI` で読ませれば、そこにある定義 (`VendorId`・`ProductId`・`GUID`・`Receiver*`) から項目を作ることもできます。優先は `devices.json` → `DRIVER_HOST_INI` → 組み込みの順です。

- Windows と macOS では、DriverHost_PX4 の定義に無い機種は開けません。足すときは DriverHost_PX4.ini と `devices.json` の両方に書きます
- Linux では、px4_drv が `/dev/<devname><N>` を作る機種だけが開けます。`devices.json` に足すのは、fork に新しい機種を足したときと、`types` を変えたいときです

## 4. Linux: px4_drv のデバイスファイル

### 4.1 デバイスファイルと受信機

px4_drv は、機種ごとの名前のデバイスファイルを受信機の数だけ作ります。番号の並びと受けられる方式は機種で決まっています ([3.2](#32-組み込みの表))。

- 機材が 2 台以上あると番号は続きます (PX-W3U4 が 2 台なら `px4video0`〜`7`)。筐体の中の受信機の番号は、[4.2](#42-機材の見つけ方) の sysfs で決めます
- udev の既定は `GROUP="video", MODE="0664"` です。エージェントを動かすユーザーを `video` グループに入れるか、コンテナに `/dev` を渡します ([16](#16-配布))
- 各デバイスファイルが受けられる方式は、ドライバが `system_cap` として持ちます (PX4 系は受信機ごとに S か T、PX-MLT 系は T・S・C・J83B、ほかは T)。受けられない方式を `PTX_SET_SYSTEM_MODE` に渡すと `EINVAL` です

### 4.2 機材の見つけ方

**`tuners.json` が無ければ、挿さっている機材を自分で見つけます。** 書いてあればそちらが勝ちます (denpa の付属のエージェントと同じ)。

1. `/dev` から、機材の定義にある `devname` で始まるデバイスファイルを集める
2. それぞれ `/sys/class/<クラス>/<名前>/device` の先を読む。クラスはデバイスファイルの名前から `video` を除いたもの (`px4video0` なら `/sys/class/px4/px4video0`、`pxmlt5video0` なら `/sys/class/pxmlt5/pxmlt5video0`)。先は USB インターフェースのディレクトリで、その親が USB デバイス。`idVendor`・`idProduct`・`serial`・`busnum`・`devpath` を読む
3. USB デバイスごとに受信機をまとめ、デバイスファイルの番号順に `#0` から番号を振る。機種は `idVendor:idProduct` を機材の定義の `usb` と突き合わせて決める (同じ `devname` に機種が複数あるときのため。`px4video` は PX4 系が使う)
4. `pairBySerial` の機種は、`serial` の先頭 14 桁が同じで末尾が `1` と `2` の 2 つの USB デバイスを同じ筐体にする。末尾 `1` の側を先に並べ、`#0`〜`#3` と `#4`〜`#7` にする。片方しか無ければ、その側だけを並べる

sysfs が読めなければ (権限、コンテナで `/sys` が無い)、デバイスファイルの名前だけで機種を決め (`devname` が 1 機種に決まるときだけ)、番号は名前の数字のままにします。

チューナーの定義 (`TunerSpec`) の中身。

| 項目       | 値                                                                                                                                 |
| ---------- | ---------------------------------------------------------------------------------------------------------------------------------- |
| `name`     | `<機種名> <USB のポート> #<受信機>`。例: `PLEX PX-MLT5PE 4-12.1 #0`、`PLEX PX-W3U4 1-2 #2`。sysfs が読めなければ `<機種名> #<番号>` |
| `types`    | 受信機の `types` のうち `GR` `BS` `CS` にあるもの                                                                                   |
| `device`   | `px4:/dev/px4video0` (`px4:` の後ろがデバイスファイルのパス)                                                                        |
| `disabled` | false                                                                                                                              |
| `lnb`      | 無し (人が画面で `15v` を書く)                                                                                                      |

`receivable` は受信機の `types` をそのまま返し、`device` が見つかっていない (抜いてある) チューナーは null を返します。

**動いている間の抜き差し。** デバイスファイルの増減を 5 秒ごとに見ます (inotify で `/dev` を見てもよい)。

- 定義が無ければ (自動検出)、見つけたもので置き換える。選局中のチューナーが消えたら、その選局を失敗で畳む
- 定義があれば、前に無かった機材を有効なチューナーとして末尾に足す。名前が重なれば ` (2)` を付ける。抜いた機材は定義から消さない (挿し直したときに設定が戻るように)
- 変わったら `tuners` の通知を出す

### 4.3 デバイスファイルの約束 (px4_drv)

ioctl の定義は px4_drv の `include/ptx_ioctl.h` です。値はそのまま使います。

| ioctl                       | 番号                                           | 引数                                     | 何                                                                                            |
| --------------------------- | ---------------------------------------------- | ---------------------------------------- | --------------------------------------------------------------------------------------------- |
| `PTX_SET_SYSTEM_MODE`       | `_IOW(0x8d, 0x0b, int)`                        | `0x10` ISDB-T / `0x20` ISDB-S / `0x40` ISDB-C / `0` 未指定 | 次の `PTX_SET_CHANNEL` の方式。受けられない方式は `EINVAL`                  |
| `PTX_SET_CHANNEL`           | `_IOW(0x8d, 0x01, struct ptx_freq)`            | `{ int freq_no; int slot; }`             | 選局してロックを待つ。ロックしなければ `EAGAIN`                                               |
| `PTX_START_STREAMING`       | `_IO(0x8d, 0x02)`                              | 無し                                     | TS の受信を始める。既に始めていれば `EALREADY`                                                 |
| `PTX_STOP_STREAMING`        | `_IO(0x8d, 0x03)`                              | 無し                                     | 止める。止まっていれば `EALREADY`                                                             |
| `PTX_GET_CNR`               | `_IOR(0x8d, 0x04, int *)`                      | `u32` を受ける                           | C/N の生の値 (PT1/PT3 互換)。[7.4](#74-受信の状態と-cn)                                       |
| `PTX_ENABLE_LNB_POWER`      | `_IOW(0x8d, 0x05, int)`                        | `1` 11V / `2` 15V                        | 衛星の給電。対応しない機種は `ENOSYS`                                                          |
| `PTX_DISABLE_LNB_POWER`     | `_IO(0x8d, 0x06)`                              | 無し                                     | 給電を止める                                                                                  |
| `PTXT_SET_PARAMS`           | `_IOW(0xe7, 0x02, struct ptxt_params)`         | 方式・中心周波数 (kHz)・追加パラメータ   | ISDB-C の TSMF の 1 本目以降と J.83B の選局に使う。`reserved` は 0                              |
| `PTXT_TUNE`                 | `_IO(0xe7, 0x04)`                              | 無し                                     | `PTXT_SET_PARAMS` の値で選局。ロックしなければ `EAGAIN`、指定の TS が無ければ `ENODATA`        |

`ptx_ioctl.h` にある `PTX_GET_LOCK_STATUS`・`PTX_GET_TMCC_*`・`PTXT_GET_INFO`・`PTXT_READ_STATS` は、2026-11 時点の fork では実装されていません (`ENOSYS`)。使いません。

- **64 ビットのプロセスから呼びます。** `compat_ioctl` が無いので、32 ビットのバイナリでは ioctl が通りません。Go のビルドは `GOARCH=amd64` と `arm64` だけです
- デバイスファイルは `O_RDWR` で開きます。同じファイルを 2 度開くと `EALREADY` で失敗します (ドライバが排他する。recpt1 や Mirakurun が開いているチューナーも同じ)。最初の選局で開き、選局を跨いで開いたままにし、閉じるのは[チューナーを手放すとき](#84-選局の寿命)だけです
- `read()` はブロックします。リングバッファは 188 × 2048 バイト (モジュールパラメータ `tsdev_max_packets`、既定 2048。地上波で 0.2 秒ぶん) で、読むのが追いつかないとドライバが書き込みを捨てます (`read()` には伝わらない)。**溢れを知る手段は連続性カウンタだけ**なので、読み出しの goroutine は `read()` だけをして、復号や配信は別の goroutine に渡します。読み出しは 188 × 816 バイト (IT930x の転送 1 回ぶん) を単位にします
- TSMF で選んだ低レートの TS では、200 ミリ秒たつと要求した量に満たなくても `read()` が返ります。短い読みも正しい TS です
- 選局したあとに `PTX_STOP_STREAMING` → `PTX_SET_CHANNEL` → `PTX_START_STREAMING` と呼べば、開いたまま選局し直せます。`PTX_START_STREAMING` でリングバッファは空になります
- デバイスファイルを閉じると、ISDB-C と J.83B の方式の指定は未指定に戻ります。開いたまま使うので、選局のたびに方式を指定し直します ([7.2](#72-選局の手順))
- USB が抜かれると、ioctl と `read()` が `EIO` で返ります。その選局は失敗で畳み、チューナーを抜けたものにします

### 4.4 chardev の選局

[7.2](#72-選局の手順) の手順を ioctl に当てると次のとおりです。

1. 前の選局が残っていれば `PTX_STOP_STREAMING` (`EALREADY` は無視)
2. 衛星なら給電の ioctl。衛星でない選局では給電を触らない
3. `GR` `BS` `CS` と ISDB-C の 0 本目: `PTX_SET_SYSTEM_MODE` → `PTX_SET_CHANNEL`。ISDB-C の 1 本目以降と `SKY`: `PTXT_SET_PARAMS` → `PTXT_TUNE`
4. `PTX_START_STREAMING`
5. `read()` を始める

ドライバの中でロックを待つ時間は、地上波と衛星で最長 3 秒、ISDB-C と J.83B で 1 秒です。

## 5. Windows・macOS: DriverHost_PX4 のクライアント

**Windows と macOS では、px4_drv の DriverHost_PX4 (USB を持つ常駐のプロセス) のクライアントになります。** BonDriver_PX4 (Windows) と px4rec (macOS) と同じプロトコルで、制御・TS・カードの 3 本のパイプを使います。プロトコルの定義は px4_drv の `host/common/command.hpp` と `host/common/card_command.hpp` です。

| パイプ   | Windows (名前付きパイプ、メッセージ型)   | macOS (UNIX ドメインソケット)                 | 役目                                                   |
| -------- | ---------------------------------------- | --------------------------------------------- | ------------------------------------------------------ |
| 制御     | `\\.\pipe\px4_ctrl_pipe`                 | `$PX4_RUNTIME_DIR/px4_ctrl_pipe.sock`         | 受信機を開く・選局・ロック・C/N・LNB。**1 接続に受信機 1 つ** |
| TS       | `\\.\pipe\px4_data_pipe` (読みはバイト型) | `$PX4_RUNTIME_DIR/px4_data_pipe.sock`         | `SET_DATA_ID` のあと TS が流れる                       |
| カード   | `\\.\pipe\px4_card_pipe`                 | `$PX4_RUNTIME_DIR/px4_card_pipe.sock`         | 内蔵カードリーダー                                     |

`PX4_RUNTIME_DIR` の既定は `/tmp` です。

### 5.1 接続と起動

- Windows: `WaitNamedPipeW` → `CreateFileW` (`GENERIC_READ | GENERIC_WRITE`、`FILE_FLAG_OVERLAPPED`) → `SetNamedPipeHandleState` で制御とカードは `PIPE_READMODE_MESSAGE`、TS は `PIPE_READMODE_BYTE`。`ERROR_PIPE_BUSY` なら繰り返す。Go では `golang.org/x/sys/windows` を使う
- Windows で DriverHost_PX4 が動いていなければ起動します。手順は BonDriver と同じで、イベント `Global\DriverHost_PX4_StartupEvent` を `CreateEventW` し、`ERROR_ALREADY_EXISTS` でなければ `DRIVER_HOST_PATH` (既定はエージェントの実行ファイルと同じフォルダの `DriverHost_PX4.exe`) を `CreateProcessW` して、イベントが立つのを 10 秒待ちます。DriverHost_PX4 は `Global\DriverHost_PX4_Mutex` で 1 つしか動きません。同じ利用者のセッションで動かします (サービスとして動かすときは DriverHost_PX4 も同じセッションに置く)
- macOS: `socket(AF_UNIX, SOCK_STREAM)` → `connect`。DriverHost_PX4 は root で先に起動しておきます (launchd)。エージェントは起動しません (一般ユーザーで起動しても USB を取れないため)。繋がらなければ `/denpa/tuners` を空にし、記録に `DriverHost_PX4 に繋がりません (<socket>)` を出して 5 秒ごとに繋ぎ直します
- 制御とカードは「送って同じ長さを受ける」往復です。Windows は 1 メッセージが 1 コマンドです。macOS のソケットには境目が無いので、送った長さぶんを読み切ります (`recv` を繰り返す)
- パイプが切れたら (`read` が 0・`EPIPE`・`ERROR_BROKEN_PIPE`)、その受信機の選局を失敗で畳み、チューナーを「開いていない」に戻します。DriverHost_PX4 は USB の受信が全部止まると TS のパイプを閉じるので、それも同じ扱いです

### 5.2 コマンドの構造体

`#pragma pack(push, 8)` の C++ の構造体をそのまま送ります。整数はリトルエンディアン、`bool` は 1 バイト、`wchar_t` は Windows で 2 バイト (UTF-16LE)、macOS で 4 バイト (UTF-32LE)、`GUID` は `Data1 u32`・`Data2 u16`・`Data3 u16`・`Data4 [8]u8` (16 バイト、4 バイト境界)。

制御 (`CtrlCmdHeader` は `cmd u32` + `status u32`。`status` は送るとき 0、応答で 1 が成功、2 が失敗)。

| コマンド              | `cmd` | 構造体と大きさ (Windows / macOS)                                                                                         | 中身                                                                                                                                              |
| --------------------- | ----- | ------------------------------------------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET_VERSION`         | 1     | `CtrlVersionCmd` 16 / 16                                                                                                 | 応答の `driver_version`・`cmd_version` (`0x00040004`)。違えば起動を止めて記録に出す                                                                |
| `OPEN`                | 8     | `CtrlOpenCmd` 436 / 820 (`ReceiverInfo` 428 / 812)                                                                        | 鍵: `device_guid` (機種の GUID)、`index` (受信機の番号。−1 なら何でも)、`systems` (方式のビット和)。`device_name`・`receiver_name`・`receiver_guid` は空。応答に `data_id` が入る |
| `CLOSE`               | 9     | 8 / 8                                                                                                                    | 受信機を閉じる                                                                                                                                    |
| `GET_INFO`            | 10    | `CtrlReceiverInfoCmd` 436 / 820                                                                                           | 開いている受信機の情報                                                                                                                            |
| `SET_CAPTURE`         | 11    | `CtrlCaptureCmd` 12 / 12 (`bool capture` は 9 バイト目)                                                                   | TS の受信の開始と停止                                                                                                                             |
| `SET_PARAMS`          | 17    | `CtrlParamsCmd` 28 / 28 + 追加 8 × (num − 1)                                                                              | `system u32`・`freq u32` (kHz)・`num u32`・`params[]` (`type u32`・`value u32`)。`type` は `BANDWIDTH` 1・`TSMF_STREAM` 2・`STREAM_ID` 16            |
| `CLEAR_PARAMS`        | 18    | 8 / 8                                                                                                                    |                                                                                                                                                   |
| `TUNE`                | 19    | `CtrlTuneCmd` 12 / 12                                                                                                    | `timeout u32` (ミリ秒)。ロックしなければ失敗                                                                                                      |
| `CHECK_LOCK`          | 20    | `CtrlCheckLockCmd` 12 / 12                                                                                               | 応答の `locked` (9 バイト目)                                                                                                                       |
| `SET_LNB_VOLTAGE`     | 24    | `CtrlLnbVoltageCmd` 12 / 12                                                                                              | `voltage i32` (0 / 11 / 15)                                                                                                                        |
| `READ_STATS`          | 32    | `CtrlStatsCmd` 20 / 20 + 追加 8 × (num − 1)                                                                               | `num u32`・`data[]` (`type u32`・`value i32`)。`type` は `SIGNAL_STRENGTH` 1・`CNR` 2。C/N は mdB (1000 で割ると dB)                                  |

`ReceiverInfo` の並び: `device_name [96]wchar`・`device_guid`・`receiver_name [96]wchar`・`receiver_guid`・`systems u32`・`index i32`・`data_id u32`。方式のビットは `ISDB_T 0x10`・`ISDB_S 0x20`・`ISDB_C 0x40`・`J83B 0x80`。

TS のパイプの `DataCmd` (8 バイト): `cmd u32` (`SET_DATA_ID` 1・`PURGE` 8) + `data_id u32`。`SET_DATA_ID` のあとは TS が 188 × 1024 バイトまでの塊で流れ、応答はありません。

カードの `Command` は固定長 1 つで、Windows 4,952 バイト、macOS 9,304 バイトです。並びは `code u32`・`status u32`・`error u32`・`version u32`・`reader_count u32`・(詰め物 4)・`reader_generation u64`・`readers [16][128]wchar`・`reader_name [128]wchar`・`share_mode u32`・`disposition u32`・`card_present bool`・`card_initialized bool`・`atr [33]u8`・(詰め物)・`atr_length u32`・`data [512]u8`・`data_length u32`・`data_capacity u32`。サーバは長さが違う要求を切るので、大きさは単体テストで固定します (`unsafe.Sizeof` ではなく、OS ごとの定数で持つ)。

| コマンド            | `code` | 中身                                                                                                                  |
| ------------------- | ------ | --------------------------------------------------------------------------------------------------------------------- |
| `GET_VERSION`       | 1      | 応答の `version` (`0x00020001`)                                                                                        |
| `LIST_READERS`      | 2      | `reader_count` と `readers[]` (内蔵リーダーの名前。`PLEX PX-MLT5PE Smart Card Reader (ID: …)` のような形)              |
| `CONNECT`           | 3      | `reader_name`・`share_mode` (`SHARED` 1 / `EXCLUSIVE` 2)。カードが無ければ `error` `NO_CARD` 3                        |
| `RECONNECT`         | 4      | `share_mode` を変える                                                                                                 |
| `DISCONNECT`        | 5      | 手放す                                                                                                                |
| `STATUS`            | 6      | `reader_name` を渡し、`card_present`・`card_initialized`・`atr`・`atr_length` を受ける。繋いでいないリーダーにも使える |
| `RESET`             | 7      | カードをリセットし ATR を受ける                                                                                       |
| `TRANSMIT`          | 8      | `data`・`data_length` に APDU、`data_capacity` に受ける上限。応答の `data`・`data_length` が応答 (SW1 SW2 込み)       |
| `BEGIN_TRANSACTION` | 9      | 排他の開始                                                                                                            |
| `END_TRANSACTION`   | 10     | 排他の終了。`disposition` (`LEAVE` 0 / `RESET` 1)                                                                     |

`error` は `NONE` 0・`INVALID_PARAMETER` 1・`NO_READER` 2・`NO_CARD` 3・`BUSY` 4・`REMOVED` 5・`TIMEOUT` 6・`PROTOCOL` 7・`INSUFFICIENT_BUFFER` 8・`INTERNAL` 9 です。

### 5.3 機材の見つけ方

DriverHost_PX4 には受信機を並べるコマンドが無いので、USB を自分で数えます。

- Windows: 機材の定義の `host.deviceGuid` に対応する DriverHost_PX4.ini の `DeviceInterfaceGUID` を `DRIVER_HOST_INI` から読み、`SetupDiGetClassDevsW(guid, DIGCF_DEVICEINTERFACE | DIGCF_PRESENT)` と `SetupDiEnumDeviceInterfaces` で挿さっている数を数える (DriverHost_PX4 の `DeviceManager::Search` と同じ)。`DRIVER_HOST_INI` が無ければ、`SetupDiGetClassDevsW(GUID_DEVINTERFACE_USB_DEVICE)` で USB デバイスを並べ、ハードウェア ID の `VID_xxxx&PID_xxxx` を `usb` と突き合わせる
- macOS: IOKit (`IOServiceMatching("IOUSBDevice")` に `idVendor`・`idProduct`) で数える。`USB Serial Number` も読む
- 機種 1 つにつき、挿さっている台数 × 受信機の数のチューナーを並べる。名前は `<機種名> #<受信機>`、2 台目以降は `<機種名> (2) #<受信機>`
- `device` は `px4host:<機種の GUID>:<受信機の index>:<台目>` (`px4host:{aa8de3fc-…}:2:1`)
- **同じ機種が 2 台あるとき、どちらの筐体の受信機を開くかは DriverHost_PX4 が決めます** (OPEN は条件に合う空いている最初の受信機を返す)。チューナーの名前と筐体は結び付きません。PX-Q3 系も 2 つの USB デバイスを区別しません。LNB の給電は開いた受信機の筐体に効きます
- 抜き差しは 5 秒ごとに数え直します (Windows は `WM_DEVICECHANGE` を使ってもよい)。選局中に台数が減って OPEN していた受信機が消えると、パイプが切れるのでそこで畳みます

### 5.4 host の選局

1 つのチューナーが制御のパイプを 1 本持ち、受信機を開いたままにします (chardev と同じく、最初の選局で開いて手放すまで)。

1. 最初の選局で: 制御のパイプを繋ぎ、`GET_VERSION` → `OPEN` (`device_guid`・`index`・`systems` は受信機の定義の方式のビット和)。失敗したら `<チューナー名>: 受信機を開けません (DriverHost_PX4 に空きがありません)` で 500
2. TS のパイプを繋ぎ、`SET_DATA_ID` に `OPEN` の応答の `data_id` を書く (応答は無い)。`SET_CAPTURE` の前に繋ぐ。**TS のパイプは受信機を手放すまで繋ぎっぱなしにし、読む goroutine を 1 本持つ。** 選局のたびに繋ぎ直すと DriverHost_PX4 に切られる。選局の読み手は、その goroutine の列から取る。読み手を外せば読み待ちは解ける
3. `SET_CAPTURE(true)` を最初の選局で 1 回送る (BonDriver と同じ。選局の前)。**選局し直すときに `SET_CAPTURE(false)` で止めない。** 衛星なら `SET_LNB_VOLTAGE(15 | 11 | 0)` (`lnb` の無い機種では送らない)
4. `SET_PARAMS`: `system` と `freq` (kHz) と追加パラメータ ([7.3](#73-チャンネル名から選局の値へ) の表の host の列)。`TUNE(timeout)` は 5,000 ms (ISDB-C と J.83B は DriverHost_PX4 の中で 1 秒でロックを打ち切る)。失敗は「同期しませんでした (…)」。TSMF の TS が無いときも `TUNE` が失敗するだけで区別できないので、同じ文言にする
5. 列に溜まった前のチャンネルの残りを捨て、`PURGE` を 1 回送る。`PURGE` のあとの 1 塊も捨てる (BonDriver の `NumberOfBuffersToIgnoreAfterPurge`)
6. C/N は `READ_STATS` の `CNR` (mdB)、ロックは `CHECK_LOCK`。`READ_STATS` が失敗 (`status` 2) する機種は、C/N を null にする
7. 選局を終えたら (`Stop`) 読み手を外すだけで、受信は止めない。手放すとき (`Close`) に `SET_CAPTURE(false)` → `SET_LNB_VOLTAGE(0)` → `CLOSE` → パイプを閉じる

Windows のパイプは `FILE_FLAG_OVERLAPPED` で開く ([5.1](#51-接続と起動))。同期 I/O のハンドルでは、TS のパイプの読み待ちの間、同じハンドルへの `PURGE` の書き込みが塞がる (実機で選局が固まった)。

DriverHost_PX4 の `SET_PARAMS` は方式を `systems` に含む受信機でだけ通ります。PX-MLT 系の受信機は定義が `ISDB-T,ISDB-S` でも DriverHost_PX4 が ISDB-C と J.83B を足します。

## 6. コマンド (recpt1 互換の CLI) のバックエンド

**recpt1 のように「チャンネルを引数に取り、TS を標準出力に流し続けるコマンド」も、チューナーとして使えます** (`cmd` バックエンド)。px4_drv 以外の chardev ドライバ (PT3 の chardev 版、PLEX の公式ドライバ、Mirakurun で動いている recpt1 の設定) や、macOS の px4rec を、専用のバックエンドを書かずに繋ぐ逃げ道です。denpa の付属のエージェントの `FAKE_TUNE` (適合テスト用) も、このバックエンドの 1 つの設定として持ちます。

- 1 回の選局 = 子プロセス 1 つ。標準出力を TS (か TLV) として読む。非 0 で終わったら「同期しませんでした (…)」。選局し直しは殺して起動し直す (recpt1 で 0.5〜1 秒)
- 止めるときは `SIGTERM` をプロセスグループごと送り、5 秒で終わらなければ `SIGKILL`。Linux と macOS だけ (Windows では使えない。シグナルで止められず、recpt1 系も無い)
- 復号はエージェントで行うので、CLI は復号なし (`--b25` を付けない) で呼ぶ。局を絞る引数 (`--sid`) も付けない (TS が丸ごと要る)
- 受信の状態と C/N は出せない (`signal` は null)。LNB は引数に写すだけ。機材の自動検出は無く、`tuners.json` に `device` を手で書く
- **コマンドの雛形は `devices.json` と環境変数にだけ書き、画面 (`PUT /denpa/tuners`) からは受け取らない。** denpa のエージェントの「選局コマンドは画面から渡させない」を守る (受け取ると、denpa に入れた人が機材側で好きなコマンドを走らせられる)。`PUT /denpa/tuners` で受けるのは `device` の `cmd:<機種の model>:<デバイス>` だけ

### 6.1 機材の定義の書き方

`devices.json` の機種に `command` を書くと、その機種は `cmd` バックエンドになります。

```json
{
  "model": "recpt1 (PT3)",
  "command": ["recpt1", "--device", "{device}", "{lnb}", "{channel}", "-", "-"],
  "channelFormat": "recpt1",
  "lnbArg": { "15v": ["--lnb", "15"], "11v": ["--lnb", "11"], "": [] },
  "receivers": [{ "index": 0, "types": ["GR", "BS", "CS"] }]
}
```

| 項目            | 意味                                                                                                                                                                               |
| --------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `command`       | 引数の配列 (シェルは通さない)。`{device}` は `tuners.json` の `device` の `cmd:<model>:` より後ろ、`{channel}` は `channelFormat` で直したチャンネル名、`{lnb}` は `lnbArg` の引数 (配列なので複数に展開。空なら消える) |
| `url`           | `command` の代わりに書く (同時には書けない)。`http://` で始め、`command` と同じ `{device}`・`{channel}`・`{sidQuery}` などを展開した URL に、エージェントが GET して TS を読む。503 は使用中 (409)、404 は同期しなかった ([6.5](#65-sony-nasne-sonynasne-debian-の-nasne-recpt1)) |
| `channelFormat` | チャンネル名の直し方。`recpt1`・`recisdb`・`px4rec`・`denpa` (そのまま) のどれか ([6.2](#62-チャンネル名の直し方))                                                                   |
| `lnbArg`        | `lnb` の設定 (`15v`・`11v`・空) ごとの引数。無ければ `{lnb}` は消える                                                                                                              |
| `receivers`     | 1 つだけ書く。`types` がその機材の `receivable`                                                                                                                                    |
| `env`           | 子プロセスに足す環境変数 (任意)                                                                                                                                                    |

`tuners.json` の 1 台は `{"name": "PT3 T0", "types": ["GR"], "device": "cmd:recpt1 (PT3):/dev/pt3video2"}` のように書きます。`device` の末尾が `{device}` に入ります。

### 6.2 チャンネル名の直し方

| `channelFormat` | `T27`  | `C30` | `BS15`   | `BS15:16625`         | `CS02` | `QC30:2`        | `H44`       |
| --------------- | ------ | ----- | -------- | -------------------- | ------ | --------------- | ----------- |
| `recpt1`        | `27`   | `C30` | `BS15_0` | 断る (400。下を参照) | `CS2`  | 断る            | 断る        |
| `recisdb`       | `T27`  | `C30` | `BS15_0` | 断る                 | `CS02` | `ISDBC-C30_2`   | `J83B-H044` |
| `px4rec`        | `T 27` | 断る  | `S 7`    | 断る                 | `S 12` | `C 30 --tsmf 2` | `J 44`      |
| `denpa`         | そのまま (`T27`・`BS15:16625`・`QC30:2`)。`FAKE_TUNE` 用。種別も `{type}` で渡せる                                                                 |

- **TSID 指定 (`BS15:16625`) は recpt1・recisdb には渡せません。** これらは相対 TS 番号 (`BS15_0`) しか取らず、TSID と相対番号の対応は NIT を読まないと分からないためです。`:TSID` の無い衛星の選局 (`BS15` → `_0`) だけ通し、`:TSID` 付きは 400 `<チューナー名>: このコマンドでは TSID を指定できません` で断ります。denpa は衛星の局の選局に `:TSID` を付けるので、**recpt1・recisdb のチューナーでは衛星は番組表の入口 (`BS15`) しか受けられません。** README の既知の制限に書き、`types` から `BS` `CS` を外すことを勧めます
- 引数が 2 つ以上になる書き方 (`px4rec` の `T 27`) は、`{channel}` を配列として展開します
- `recpt1` の `C30` はパススルー (地上波) です。ISDB-C は recpt1 では選局できません

### 6.3 選局の流れ

1. 前の子プロセスが残っていれば止める (`SIGTERM` → 5 秒 → `SIGKILL`)
2. 引数を組み立てて起動する。標準エラーは記録に流す (行の先頭にチューナーの名前を付ける。C/N の行もそのまま残る)
3. 標準出力を TS として読む。最初の TS を 30 秒待つのは他と同じ。先に非 0 で終わったら、その時点で「同期しませんでした (…)」
4. 利用者が 0 人になって余韻 (5 秒) が過ぎたら止める。選局を跨いで起動したままにはしない (recpt1 は 1 回の選局で 1 回の起動)

`FAKE_TUNE` は、`{"model": "fake", "command": ["<FAKE_TUNE>", "{type}", "{channel}"], "channelFormat": "denpa"}` を組み込みに持ち、`FAKE_TUNE` が書いてあるときは `tuners.json` の `device` の無いチューナーを全部この機種として扱います (付属のエージェントと同じ)。

### 6.4 1 サービスしか出ない機材と、機材の中で復号する機材

Sony nasne の `nasne-recpt1` のように、「1 回の起動で 1 つの局 (サービス) しか出さず、復号も機材の中で済ませる」コマンドがあります。denpa のエージェントの約束 (物理チャンネルを丸ごと流し、同じチャンネルの利用者は 1 本を共有し、復号はエージェントが行う) と合わないので、機材の定義に印を付けて扱いを変えます。

**`singleService: true` (1 サービスしか出ない)**

- 鍵の要る利用者 (`rec`・`live`) は `service` が必須。無ければ機材に触る前に 400 `<チューナー名>: この機材は観る局 (service) が要ります`。**denpa は録画の選局に `service` を付けないので、denpa 側を直します** (録画は局を知っているので、`rec` の選局にも `service=<局の service_id>` を付ける。`src/lib/server/tuner.ts` の `openChannel` を呼ぶ録画の側)
- 鍵の要らない利用者 (`epg`・`logo`・`scan`) は `service` 無しで開く。コマンドには局を渡さず、機材が出す最初の局 + SI (PAT・NIT・SDT・EIT) を読む。番組表とスキャンは SI だけで足りる
- 選局の単位は「種別 + チャンネル + 局」。共有できるのは同じ局の利用者どうしと、鍵の要らない利用者 (どの局のストリームにも SI が入るので、同じチャンネルのどの選局にも相乗りできる)。同じチャンネルの別の局は別の選局で、チューナーが 1 つなら 409
- コマンドへの渡し方は 3 つ。`{service}` は局の番号 (10 進。無ければ空)、`{sidQuery}` は `?sid=<番号>` (無ければ空)、`serviceArg` は `lnbArg` と同じ形の引数の配列 (局があるときだけ `command` の末尾に足す)
- `receivable` と `rank` の扱いは変わらない。ストリームの `Content-Type` は TS のまま

**`decryptedByDevice: true` (機材の中で復号する)**

- 復号の経路を通さず、受けた TS をそのまま流す (PAT・PMT・ECM・CAT・EMM も読まない)。EMM はカードに渡さない
- `/denpa/card` は `{"ok": true, "message": "", "source": "local", "readers": [{"name": "<機種名> (機材の中で復号)", "card": "B-CAS", "ids": [], "active": true, "tuners": [<復号中のチューナー>], "disabled": false}]}` を返す。denpa はこれを「カードを読めている」と見て録画とライブを振り分ける。番号が空なので、契約を確かめたカードの順では「契約を確かめていないカード」(無料の局と同じ扱い) になる
- `POST /denpa/card/confirm` は 500 `この機材では契約を確かめられません`。`POST /denpa/decode` も 500 `この機材で録った TS は復号済みです`
- `cards.json` の「使わない」は、この擬似のリーダーにも効く (外すと `ok: false` で、録画とライブは来なくなる)

**`busyExitCode`**

コマンドが「チューナー使用中」を表す終了コード (nasne-recpt1 は 3)。この終了コードで終わったときは「同期しなかった」とせず、409 `<種別> のチューナーに空きがありません` で返し、そのチューナーを組の後ろにも回さない。`url` の機種では使わない (書くと断る)。HTTP のステータスで決まる ([6.5](#65-sony-nasne-sonynasne-debian-の-nasne-recpt1))。

### 6.5 Sony nasne (SonyNasne-debian の `nasne-recpt1`)

[SonyNasne-debian](https://github.com/mar1mo-41414/SonyNasne-debian) は、nasne (CECH-ZNR2J。MIPS 74Kc、mipsel、RAM 248 MB) に Debian を直に起動し、内蔵チューナーを `nasne-recpt1` (recpt1 互換のコマンド。`--listen 8301` で HTTP でも配る) で使えるようにしたものです。TS は nasne のファームが B-CAS で復号したものが出ます。

`nasne-recpt1` の約束 (2026-11 の `scripts/tools/nasne_recpt1.c`)。

- `nasne-recpt1 [--sid N] [--tsid N] [--dual-tuner] <13〜62 | BS1〜BS23 (BS15_0 のようにスロット付きも可) | CS2〜CS24 | --freq MHz> <秒数|-> <出力|->`。`--b25`・`--device`・`--lnb` は受けて無視する
- 1 回の起動で 1 サービス (`--sid`。無ければ最初の映像の局) + SI。ワンセグは出さない
- 既定は地デジも衛星も 1 系統 (`/tmp/nasne-recpt1.lock`)。`--dual-tuner` で地デジ 1 + 衛星 1 を同時に使える (試験的。既知の問題あり)
- 終了コード: 0 正常、1 エラー、2 使い方、3 チューナー使用中。ロックを待つのは 4 秒
- HTTP: `GET /tuner/<ch>?sid=0x…` (`<ch>` はコマンドラインと同じ記法)、`GET /scan`、`GET /status` (`idle` / `busy`)。使用中は 503

2 つの置き方のどちらでも、[6.4](#64-1-サービスしか出ない機材と機材の中で復号する機材) の `singleService` と `decryptedByDevice` と、denpa 側の「録画にも `service` を付ける」直しが要ります。

#### 置き方 A: エージェントは別の機械に置き、nasne を HTTP で呼ぶ (勧める)

denpa のある機械 (か、同じ LAN の Linux・macOS・Windows) でエージェントを動かし、`cmd` バックエンドの `url` (エージェントが HTTP で直接読む。`curl` は使わない) で nasne を呼びます。**エージェントを nasne に足さない**ので、MIPS のビルドもカーネルの制限も、TLS の暗号の負荷も nasne には関わりません (nasne 側で動かすのは、今までどおり `nasne-recpt1 --listen 8301` の HTTP だけ)。nasne とエージェントの間は平文の HTTP で、TLS になるのはエージェントと denpa の間です。**負荷の面からも、こちらを勧めます** (置き方 B の負荷は下)。

組み込みの機種表に `Sony nasne (nasne-recpt1 HTTP)` があります。`devices.json` に書く必要はありません。組み込みの定義は次のとおりです。

```json
{
  "model": "Sony nasne (nasne-recpt1 HTTP)",
  "url": "http://{device}:8301/tuner/{channel}{sidQuery}",
  "channelFormat": "recpt1",
  "singleService": true,
  "decryptedByDevice": true,
  "receivers": [{ "index": 0, "types": ["GR", "BS", "CS"] }],
  "lnb": [],
  "cardReader": false
}
```

- `tuners.json` は `{"name": "nasne", "types": ["GR", "BS", "CS"], "device": "cmd:Sony nasne (nasne-recpt1 HTTP):nasne.local"}` の 1 台 (既定の 1 系統)。nasne 側を `--dual-tuner` で動かすなら、`types` を `["GR"]` と `["BS", "CS"]` に分けた 2 台にする (同じ `device`)
- `url` の機種は、HTTP のステータスで失敗を分ける ([#17](https://github.com/maimainoue/denpa-px4-agent/issues/17))。**503 は使用中 (409 `<種別> のチューナーに空きがありません`)、404 は同期しなかった (電波なし・サービスなし。放送休止の判定に使う文言)、200 は TS を流す。繋がらない・ほかのステータス (500 など。B-CAS が挿さっていない nasne の `ACTIVATE失敗` など) は、使用中でも同期しなかったでもない失敗 (500) で、`機材が HTTP <ステータス> を返しました` と理由を返す。** 200 でないときの本文の先頭 200 バイトは記録に出す。TS は本文として直接読むので、`curl` は要らない (Windows や Docker のイメージでも動く)。リダイレクトは追わず、環境のプロキシも使わない。選局のたびに新しく繋ぎ、前の接続は切る。`channelFormat: recpt1` が `27`・`BS15_0`・`CS2` を作る。`url` は `http://` だけで、`{device}` が入る url の機種は、device の末尾をホスト名か IPv4 に限る (6.1 の `command` と同じ)。`command` と `url` と、`url` と `serviceArg`・`tsidArg`・`busyExitCode` は同時に書けない
- 衛星の `:TSID` は、この設定例では渡せない。`url` の機種には `tsidArg` を書けないので、**`:TSID` 付きの選局 (`BS15:16625` など) は 400 で断られる** (6.2 の制限と同じ)。`:TSID` 無しの `BS15` は `BS15_0` になる (`channelFormat: recpt1` は衛星を常に `_0` にする。スロット番号を付けた `BS15_1` などは、この経路では選べない)。nasne の HTTP に `--tsid` に当たる指定 (`tsid=`) があるかは、版によるかもしれず、実機の版では未確認 ([20](#20-決めていないこと確かめること))。あると分かれば、`tsidArg` を足して渡せる
- 選局の切り替えは前の接続を切って、新しく繋ぎ直す。nasne 側はロックを 4 秒待つので、連続の切り替えでは 503 (使用中、409) が返ることがある (denpa が掛け直す)
- nasne 側の `/status` と `nasne-waked` (予約の 5 分前に起こす) は使わない。電源を落とす運用は denpa とは合わない (録画の時刻を nasne が知らない) ので、nasne は常時起動にする

#### B-CAS なしで使う: nasne は生の TS を出し、復号はエージェントが行う

`nasne-recpt1` の `--no-bcas` (HTTP は `?bcas=0`。SonyNasne-debian の 2026-11-21 のコミット 4db1494 以降) を付けると、nasne は B-CAS を見ず (ACTIVATE も、機材の中の復号も使わず)、**復号しない生の TS (全 PID)** を出します。`--sid`・`?sid=` は無視されます。B-CAS を挿していない nasne でも選局できます (標準の機種では、B-CAS が無いと 500 `secured stream` になる)。復号は、エージェントが、自分のカードか `CARD_URL` で貰う鍵 ([9.5](#95-鍵を配る貰う)) で行います。対応していない版の `nasne-recpt1` で `bcas=0` がどう扱われるかは、確かめていません。

組み込みの機種表に `Sony nasne (nasne-recpt1 HTTP raw)` があります (`devices.json` は要りません)。

```json
{
  "model": "Sony nasne (nasne-recpt1 HTTP raw)",
  "url": "http://{device}:8301/tuner/{channel}?bcas=0",
  "channelFormat": "recpt1",
  "receivers": [{ "index": 0, "types": ["GR", "BS", "CS"] }],
  "lnb": [],
  "cardReader": false
}
```

- `tuners.json` は `{"name": "nasne", "types": ["GR", "BS", "CS"], "device": "cmd:Sony nasne (nasne-recpt1 HTTP raw):nasne.local"}`。B-CAS ありの機種 (`Sony nasne (nasne-recpt1 HTTP)`) と機種名が似ているので、`device` の `cmd:` のあとの名前を取り違えないこと (`HTTP` のあとの ` raw` の有無で別の機種になる)
- **`decryptedByDevice` も `singleService` も書かない (false)。** PX4 などと同じ経路で、エージェントが PAT・PMT・ECM を読んで復号します。`/denpa/card` は実際のカードのリーダーを出し、契約確認 (`POST /denpa/card/confirm`) と `POST /denpa/decode` も使えます。カードが無いと、鍵の要る利用者 (録画・ライブ) は復号できません。生の TS は全部の局を含むので、**同じチャンネルの別の局は、1 つの選局に相乗りできます** (nasne への HTTP は 1 本)。`service` は、復号する局を選ぶのに使い、nasne には渡しません
- 503 (使用中)・404 (同期しなかった)・その他の失敗の扱いは、B-CAS ありの機種と同じです (上の `url` の機種の決まり)
- 実機で確かめた (2026-11-21、nasne と Linux のエージェント、本物の B-CAS)。ch=21 の `?bcas=0` は、約 95% のパケットがスクランブルされたままで、CAT と複数の局 (sid 1056・1057・1058・1059) の PID を含む。エージェントで復号した局 1056 の 105,298 パケットは、スクランブルの印が 0 で、ffprobe で MPEG-2 1440×1080・AAC・字幕を確認した。局 1056 と 1057 を同時に開いても、nasne への HTTP は 1 本で、どちらも全パケットが復号された
- 負荷: nasne は B-CAS も復号も使わないので、復号の CPU は nasne に載らない (復号はエージェントを動かす機械で行う)。LAN を流れる量は、復号済みの TS と同じ (ch=21 で約 17 Mbps)。**エージェントを nasne の上で動かす置き方 B では、復号が nasne の 1 コアに載るので、勧めない**。置き方 B で使うなら、`command` に `--no-bcas` を足し (`["/usr/local/sbin/nasne-recpt1", "--no-bcas", "{channel}", "-", "-"]`)、`decryptedByDevice`・`singleService`・`serviceArg` は書かず、カードは `CARD_URL` で貰う (nasne にはカードリーダーが無い。`CARD_URL` は実装中: [#18](https://github.com/maimainoue/denpa-px4-agent/issues/18))

#### 置き方 B: エージェントを nasne の上で動かす

denpa 本体は nasne では動かない (bun の mipsel は無い) ので、動かすのはエージェントだけです。A と比べて得るものは「LAN を流れるのが TLS になる」ことだけで、負荷と手間は増えます。

- ビルドは `GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0`。74Kc は FPU を持たないので `softfloat` が要る。静的なバイナリ 1 つを `/usr/local/sbin/denpa-px4-agent` に置き、sysvinit のスクリプトで起動する
- **公式の 2.6.29 のカーネルでも動く** (実機の nasne、Debian 7、`2.6.29-ts-mipsisa32r2el` で、Go 1.27 のバイナリが起動し、TLS・API・コマンドの起動まで通った)。Go が動作を保証する下限 (2.6.32) より古いが、足りないシステムコールは `accept4` だけだった (MIPS では 2.6.32 で入った)。Go の標準ライブラリは `accept4` しか使わず `accept4: function not implemented` で待ち受けられないので、**最初の `Accept` が `ENOSYS` で失敗したら、待ち受けの fd の複製を blocking のまま `accept(2)` で待つ代替に切り替える** (`internal/app/listen_linux.go`)。`syscall.Accept` も中で `accept4` を呼ぶので、`SYS_ACCEPT` を直に呼ぶ。6.12 のカーネルは要らない
- 機材の定義は A の `command` を `["/usr/local/sbin/nasne-recpt1", "{channel}", "-", "-"]`、`serviceArg` を `["--sid", "{service}"]`、`busyExitCode` を 3 に替える。衛星の `:TSID` は `["--tsid", "{tsid}"]` で渡せる (`{tsid}` は `:TSID` の値。無ければ空。`tsidArg` として `serviceArg` と同じ形で足す)。`--dual-tuner` を使うなら `command` に足し、`tuners.json` を 2 台にする
- `nasne-recpt1 --listen 8301` を同時に動かしてもよい (同じロックを使うので取り合いは成り立つ)
- 置き場: `TUNERS_FILE` などは `/var/lib/denpa-px4-agent/`。`RAW_DIR` は使わない (録画は denpa の側に置かれる)
- **負荷 (実機の nasne で測った。仕様の見込みは当たらなかった)。** nasne は 1 コア・1 スレッドで、74Kc には AES の命令も ChaCha20 の最適化も無い。TLS (ChaCha20-Poly1305) は純 Go の実装になる。結果は次のとおりで、試験ごとに条件が違うので混ぜない。
  - **実放送のエンドツーエンド (2026-11-20、B-CAS を挿した実機)。** PC の TLS クライアント → nasne 上のエージェント (別のポート、`cmd` バックエンドで `nasne-recpt1 21 - - --sid 1056` を直に起動) で、ch=21 の約 16.5 Mbps を 30 秒流し、その間の 20 秒の CPU を測った (各構成 2 回、Go 1.27.0、`-tags nasne,nethttpomithttp2`、`GOGC=50`)。**標準版 (純 Go) は 15.8〜16.5 Mbps が流れ、エージェントは 1 コアの 88〜92% (user 72〜78%)、コア全体は 94〜97%** で、余裕がほとんど無い。`nasne-recpt1` は約 3%。TS の同期ずれは 0。受け手は PC なので、復号の負荷は nasne に載っていない
  - **以前の 12 Mbps の試験 (2026-11 前半) は再現しなかった。** 12 Mbps の入力で、流せたのは 9.6 Mbps、エージェントの CPU は 1 コアの約 60%、その約 78% (**エージェントの CPU の内訳**。コア全体では約 47%) が TLS だった。入力の経路 (実受信か合成か) とコア全体の CPU は記録に残っておらず、上の実放送の結果と合わない。**この数字は根拠にしない** (プロファイルは `-tags pprof` のビルドで `CPUPROFILE=<ファイル>` を渡し、`SIGUSR1` で書く)
  - **TLS のループバック試験 (2026-11-20)。** 同じ nasne 実機、Go 1.27.0、`linux/mipsle` (`softfloat`)、`crypto/tls` の TLS 1.3 (`TLS_CHACHA20_POLY1305_SHA256`)、socketpair の上で、送り手と受け手を**同じ 1 コアで**走らせ、2 つの合計の CPU 時間で割った。数字は**暗号化と復号の合計**で、`Write` の大きさ 32 KiB・16 KiB・1316 B で約 9.0・12.2・10.1 Mbps/CPU 秒 (各 1 回、8 MB。誤差を含む)。送り手だけの CPU 時間は測っていない。暗号化と復号のコストが同じと仮定した**概算**では約 18〜24 Mbps/CPU 秒で、AEAD だけ (`tools/aeadbench`、ブランチ `issue-3-chacha-mips-asm` で別に測った。`Seal` が約 25〜29 Mbps/CPU 秒) とほぼ合う。平文 (同じ試験で TLS を外した) は約 263 Mbps/CPU 秒 (16 MB、`Write` 32 KiB) で、`Write` 16 KiB の TLS (12.2) と比べて約 20 倍。この値は置き方 B の送信能力そのものではなく、「暗号に使える CPU はこの程度」という目安
  - **`wget` を足したときの負荷 (2026-11-20、実機の既存の mirakc 構成の経路)。** 同じ放送 (約 16 Mbps) を `wget | head` で受けると、`wget` は 1 コアの 4.2% (user 1.0、sys 3.2)、`nasne-recpt1` は 5.8%、コア全体は 18.2%。エージェントの `cmd` バックエンドは `nasne-recpt1` を直に起動するので、この `wget` は通らない。`wget` の代わりに HTTP を直に読んでも、ソケットの read (sys) は残るので、節約は 1 コアの 2〜3% 程度 ([#4](https://github.com/maimainoue/denpa-px4-agent/issues/4))
  - **denpa から視聴したときの負荷 (2026-11-20、docker の denpa-aio から `live` で ch=21 を視聴、エージェントは下のアセンブリ版)。** denpa は視聴とは別に、`logo` (放送局のロゴの取得) の利用者として、同じチャンネルの TS を丸ごと**もう 1 本**読み続ける (h264 固定にしても同じ。エージェントの `GET /denpa/tuners` の `users` が `live GR/T21` (優先度 9) と `logo GR/T21` (優先度 1))。denpa のコンテナの中で接続ごとの受信量を測ると、2 本が各 約 16.8 Mbps (合計 約 33.5 Mbps) で、1 分以上続いた。エージェントは 1 コアの 60〜66% (user 46〜51%、sys 約 14〜15%)、コア全体は 70〜72%。PC のクライアント 1 本 (約 43%) との差は、「共通の処理が約 20%、1 本ごとの暗号と書き込みが約 22%」と見ると合う (**推測**)。CPU プロファイル (73.6 秒) では、約 75% が TLS の書き込み (ChaCha20 37%、書き込みのシステムコール 18%、コピー 11%、Poly1305 9%)、握手は約 4%。**標準版 (純 Go) では、この 2 本は 1 コアに収まらない** (1 本が約 90% なので、単純計算で約 2 倍)。**送信量は、nasne の eth0 の送信カウンターでは測れない** (PC のクライアントが 16.3 Mbps を受けているのに 6.4 Mbps と数えた。ドライバーの数え方の問題)。受信側 (denpa のコンテナの netns で `ss -tin` の `bytes_received`) で測る
  - nasne の ISDB-T の 1 サービスは 10〜17 Mbps で、標準版は 1 本でもコアがほぼ埋まり、denpa から使うと 2 本になるので、**足りない**。**余裕を取るなら置き方 A を使う**
  - **改善 (検討中。未採用)。** golang/crypto の [PR 294](https://github.com/golang/crypto/pull/294) (MIPSLE のアセンブリ。Linux カーネルと WireGuard の MIPS 実装の移植で `GPL-2.0 OR MIT`) を専用の GOROOT に当て、アセンブラに `-D=GOMIPS_r2` を渡して `ROTR` を使うと、同じ実放送でエージェントは 1 コアの 42〜43%、コア全体は 50〜52% になった (約 2.1 倍効率的。AEAD 単体は約 4.5 倍、TLS のループバックは約 3.8 倍)。x/crypto 自身のテスト (RFC 8439 のベクタ、汎用実装との乱数照合、非アライン) は実機で PASS。標準の `GOMIPS` では `ROTR` は使われず、標準のビルドのままでは約 2.7 倍 (TLS のループバック)。**専用の GOROOT (またはツールチェーン) の維持が要る**ので、今のリリースのバイナリには入っていない (作者の nasne では、この版に入れ替えて denpa から使っている) ([#3](https://github.com/maimainoue/denpa-px4-agent/issues/3)、ベンチと当て方はブランチ `issue-3-chacha-mips-asm` の `tools/`)
- **メモリは足りる。** mirakc を止めてスワップを切った状態 (バッファを除いた空き 約 200 MB) で、12 Mbps 1 本・同時 2 本・遅い読み手 (溜め込みの上限 16 MiB まで) を試し、エージェントの RSS は待機 4〜8 MB、最大で約 26 MB。バッファを除いた空きは最小でも 約 188 MB だった。`STREAM_QUEUE_BYTES=16777216`・`GOMEMLIMIT=96MiB`・`GOGC=50` を環境に書く
- **ビルドは `-tags nasne,nethttpomithttp2`。** `nasne` は chardev と pcscd を外し (64 KB 減)、`nethttpomithttp2` は標準ライブラリの HTTP/2 を外す (さらに 385 KB 減。API は HTTP/1.1 だけなので影響しない)。合わせて 8.98 MB → 8.52 MB (5%)。**待機時の RSS は 7.1 MB → 4.4 MB (約 38% 減)**。自分のコードは全体の約 3% (293 KB) で、残りは標準ライブラリ (crypto/tls・runtime・net/http・encoding/json) なので、これ以上の削減は小さい
- 止めるとき (`SHUTDOWN_WAIT`) に録画が終わるまで待つのは同じ。nasne の電源を落とす仕組みとは合わせない

## 7. 選局 (共通)

### 7.1 チャンネル名

チャンネル名は denpa の付属のエージェントと同じ書き方です (denpa の `agent/Denpa.Agent/ChannelTable.cs`)。読めなければ 400 で断ります。

- `T13`〜`T62` (地上波 UHF)、`C13`〜`C63` (ケーブルのパススルー。種別は `GR`)
- `BS01`〜`BS23` (奇数)、`CS02`〜`CS24` (偶数)。`:TSID` を付けて TS を指す (`BS15:16625`)。`BS07` と `BS17` は 4K/8K なので 400
- `QV1`〜`QV12`・`QC13`〜`QC63`・`QU13`〜`QU62` (ISDB-C。種別 `CATV`)。`:k` を付けて TSMF の k 本目 (0〜14) を指す
- `H01`〜`H105` (J.83B。種別 `SKY`)

### 7.2 選局の手順

1 回の選局 (チャンネル名 → TS が流れ始めるまで) は次の順です。チューナーの goroutine が 1 つずつ行い、同じチューナーで 2 つの選局を重ねません。

1. 前の選局が残っていれば止める
2. 衛星なら、設定の `lnb` が `15v` なら 15、`11v` なら 11、無しなら 0 を給電に渡す。機種の `lnb` に無い電圧なら、機材に触る前に `<チューナー名>: この機材は <電圧> を出せません` で 400。衛星でない選局では給電を触らない
3. 方式と周波数を渡して選局する (バックエンドごと)
4. TS の受信を始める
5. 最初の TS が届いたら利用者にレスポンスヘッダを返す

ロックしなければ「同期しませんでした (電波が来ていないか、その周波数に放送がありません)」です。この文言は denpa が放送休止の判定に読むので変えません。

`SKY` でずらした周波数を持つチャンネル (`H11`〜`H12`・`H26`〜`H28`・`H44`) は、元の周波数で同期しなければずらした周波数でもう一度選局し、それでも駄目なら同期しなかったと返します。ずらす量は `H11`〜`H12` が +1000 kHz、`H26`〜`H28` が +4000 kHz、`H44` が +2200 kHz です (px4_drv の `docs/isdbc_tuning.md` にある、Digital Devices の表と BonDriver_BDA の値。地域によって、ずれた周波数で送られている場合があります)。対象のチャンネルで放送が無いときは、失敗までの時間が約 2 倍になります (host のタイムアウトは 1 回 5 秒なので、最大 10 秒)。

### 7.3 チャンネル名から選局の値へ

| 種別   | 名前                      | 方式   | chardev: `freq_no` / `slot`                                          | host: `freq` (kHz) / 追加パラメータ                                   |
| ------ | ------------------------- | ------ | -------------------------------------------------------------------- | --------------------------------------------------------------------- |
| `GR`   | `T13`〜`T62`              | ISDB-T | チャンネル + 50 / 0 (`T27` → 77)                                     | 395143 + チャンネル × 6000 / 無し                                      |
| `GR`   | `C13`〜`C22`              | ISDB-T | チャンネル − 10 / 0                                                  | 93143 + freq_no × 6000 (C22 は + 2000) / 無し                          |
| `GR`   | `C23`〜`C63`              | ISDB-T | チャンネル − 1 / 0                                                   | 93143 + freq_no × 6000 / 無し                                          |
| `BS`   | `BS01`〜`BS23` (奇数)     | ISDB-S | (番号 − 1) / 2 / `:TSID` の値か 0                                    | 1049480 + 38360 × freq_no / `STREAM_ID` = TSID か 0                    |
| `CS`   | `CS02`〜`CS24` (偶数)     | ISDB-S | 12 + (番号 − 2) / 2 / 同上                                           | 1613000 + 40000 × (freq_no − 12) / 同上                                |
| `CATV` | `QV1`〜`QV3`              | ISDB-C | 番号 − 1 / 0                                                         | 93000 + freq_no × 6000 / `BANDWIDTH` 6、`TSMF_STREAM` k                |
| `CATV` | `QC13`〜`QC22`            | ISDB-C | 番号 − 10 / 0                                                        | 111000 + (freq_no − 3) × 6000 (C22 は 167000) / 同上                   |
| `CATV` | `QV4`〜`QV12`             | ISDB-C | 番号 + 9 / 0                                                         | 4〜7ch は 173000 から、8〜12ch は 195000 から 6000 刻み / 同上          |
| `CATV` | `QC23`〜`QC63`            | ISDB-C | 番号 − 1 / 0                                                         | 225000 + (freq_no − 22) × 6000 / 同上                                  |
| `CATV` | `QU13`〜`QU62`            | ISDB-C | 番号 + 50 / 0                                                        | 473000 + (freq_no − 63) × 6000 / 同上                                  |
| `SKY`  | `H01`〜`H105`             | J.83B  | 使わない。`PTXT_SET_PARAMS` の `freq` に 93250 + (番号 − 1) × 6500   | 93250 + (番号 − 1) × 6500 / 無し。ずらした周波数での再試行 (7.2) は `H11`〜`H12` が +1000 kHz、`H26`〜`H28` が +4000 kHz、`H44` が +2200 kHz |

- 衛星の `slot` と `STREAM_ID` は、12 未満なら「TMCC の相対 TS 番号」、12 以上なら TSID そのものです。`BS15:16625` は 16625、`:TSID` の無い `BS15` は 0 (中継器の先頭の TS。denpa が NIT を読む入口)
- ISDB-C の中心周波数は 1/7 MHz を足しません (px4_drv の `docs/isdbc_tuning.md` の表)。chardev の k が 0 なら `PTX_SET_CHANNEL`、1 以上なら `PTXT_SET_PARAMS{ system: 0x40, freq, prop: [{ 2, k }] }` → `PTXT_TUNE`
- `GR` の `C13`〜`C22` は、方式を ISDB-T に指定してから渡します。方式が未指定だと衛星の番号と重なるので、方式の指定は省きません
- 受信機の `types` に無い種別の選局は、機材に触る前に 400 で断ります (`receivable` に無い)

### 7.4 受信の状態と C/N

**使用中のチューナーだけ、5 秒ごとに読みます。** 空いているチューナーは読みません。読む間は選局し直しません。

- chardev: `PTX_GET_CNR` の生の値を dB にする (PT1/PT3 の表示で使われてきた式。px4_drv の `driver/common/signal/signal_stat.c` と同じ)。ISDB-T・ISDB-C・J.83B は 24 ビットの値で P = 10·log10(5505024 / 値)、C/N = 0.000024P⁴ − 0.0016P³ + 0.0398P² + 0.5491P + 3.0965 (値が 0 なら null)。ISDB-S は 16 ビットの値で P = √(値 − 3000) / 64、C/N = −1.6346P⁵ + 14.341P⁴ − 50.259P³ + 88.977P² − 89.565P + 58.857 (3000 未満なら null)。ロックの ioctl が無いので、`locked` は「TS が 5 秒以内に届いている」で決める
- host: `READ_STATS` の `CNR` を 1000 で割る。`locked` は `CHECK_LOCK`

`signal` の JSON は `{ "locked": true, "cn": 27.3, "unit": "dB", "atTune": false, "strengthDbm": null }` です。`strengthDbm` はいつも null です。

### 7.5 TS と TLV

来るのは素の MPEG-2 TS (188 バイト) です。ISDB-C で選んだ TS が分割 TLV のときだけ、px4_drv は TLV パケットの並び (ARIB STD-B32) を出します (chardev も host も同じ共通の処理)。

- 最初の塊を読んだときに形式を決めます。先頭が `0x47` で 188 バイトごとに `0x47` が続けば TS、先頭が `0x7F` なら TLV です。どちらでもなければ 188 の境目を探して TS として同期します
- TLV は、A-CAS のカードが開けていれば、MMTP のスクランブルを復号して流します ([b61.md](b61.md))。カードが無い・A-CAS でなければ、そのまま流します。`Content-Type` は `application/x-arib-tlv` です。TS は `video/MP2T`

### 7.6 TSMF の有効な TS の数 (`X-Denpa-Tsmf-Streams`)

denpa の付属のエージェントは、ISDB-C の選局で TSMF のヘッダーにある有効な TS の数を `X-Denpa-Tsmf-Streams` で返し、denpa はそれを見て `:1` 以降をスキャンします。px4_drv は TSMF の分離を自分の中で行い、ヘッダーを出さず、数を返す手段もありません。

- 最初の実装では、このヘッダーを**返しません**。denpa は 1 本として扱い、TSMF の 2 本目以降の局は見つかりません。既知の制限として README に書きます
- 2 段目として、px4_drvに「最後の選局で見たヘッダーの有効な TS の数」を返す口を足します (chardev は ioctl、host は `READ_STATS` の新しい `type`)。名前と番号は px4_drv 側で決めてから、この文書に足します

### 7.7 選局の失敗と理由

| 起きたこと                                    | 返し方                                                                                          |
| --------------------------------------------- | ----------------------------------------------------------------------------------------------- |
| チャンネル名を読めない                        | 400 `type と channel が必要です` / `<名前>: チャンネル名を読めません`                            |
| その種別を受けられるチューナーが無い・全部塞がっている | 409 `<種別> のチューナーに空きがありません`                                             |
| ロックしない                                  | 500 `<チューナー名>: 同期しませんでした (電波が来ていないか、その周波数に放送がありません)`。[8.5](#85-同期しなかったら同じ組の別のチューナーで選局し直す) のとおり、先に同じ組の別のチューナーで試す |
| TSMF に指定の TS が無い (chardev の `ENODATA`) | 500 `<チューナー名>: TSMF に <k> 本目の TS がありません`                                         |
| デバイスを開けない                            | 500 `<チューナー名>: <デバイス> を開けません (<理由>)`。chardev の `EALREADY` と host の OPEN の失敗は「ほかのソフトが使っています」を添える |
| DriverHost_PX4 に繋がらない                   | 500 `DriverHost_PX4 に繋がりません (<パイプ>)`                                                   |
| 30 秒たっても TS が 1 パケットも届かない      | 500 `<チューナー名>: 30 秒待っても TS が届きませんでした`。そのチューナーはそのチャンネルで 1 時間後回し |
| 送り始めたあとに取り上げられた                | 接続を壊す (正常終了にしない)。[8.4](#84-選局の寿命)                                             |
| 送る前に取り上げられた                        | 409 `優先度の高い要求 (<相手の use>) にチューナーを譲りました`                                   |
| USB が抜かれた・パイプが切れた                | 送る前なら 500、送った後なら接続を壊す。理由 `<チューナー名>: 機材が抜かれました`                 |

理由は JSON の `error` に入れます (`{"ok": false, "error": "…"}`)。denpa は `error` の文字列を画面と記録に出します。

## 8. 取り合い

**ここは denpa の `docs/agent.md` の「取り合いの決まり」「選ぶ順の約束」「共有の決まり」「選局は、利用者が切るまで終わらない」をそのまま実装します。** 違えると、denpa の予約の割り振りが実行時に崩れます。バックエンドには依りません。

### 8.1 優先度

大きいほうが勝ちます。番号は denpa が `priority` で渡します (録画 10・ライブ 9・番組表いますぐ 8・スキャン 5・衛星のロゴ待ち 4・番組表 3・ロゴ 1)。エージェントは値の意味を持たず、大小だけを見ます。

### 8.2 選ぶ順 (`rank`)

- 受けられる種別がまったく同じチューナーを 1 つの組にし、組に `rank` (0 から) を付ける。**受けられる種別の数が少ない組ほど前**、数が同じなら定義に先に出てくる組が前
- 無効 (`disabled`) のチューナーは `rank` null
- 組の中の並びは定義の順。ただし、そのチャンネルで 1 時間以内に同期しなかったチューナーは後ろに回す ([8.5](#85-同期しなかったら同じ組の別のチューナーで選局し直す))

### 8.3 選局のリクエストが来たときの選び方 (`Open`)

入力は種別・チャンネル名・優先度・`use`・`after` (引き継ぐ利用者の `use` の集合)・`tuner` (名指し)・`service`。

1. **名指しがあれば**そのチューナーだけを見る。無い・無効・その種別を受けられない → 400。同じチャンネルを読んでいれば優先度にかかわらず共有。別のチャンネルを、自分と同じか優先度の高い利用者 (`after` に無いもの) が読んでいれば 409。それ以外は取り上げて選局
2. **同じ種別・同じチャンネルの選局があり、`after` に無い利用者のうち自分と同じか優先度の高いものがいれば、その選局を共有する** (どのチューナーでも。優先度の低い利用者も残る)
3. **そうでなければ `rank` の順に組を見て、その種別を受けられる最初の組の中で選ぶ。** 順は、同じチャンネルの選局 (余韻か、優先度の低い利用者しかいない) → もう合っているチューナー → ほかの余韻 → 空いているチューナー → いちばん優先度の低い相手のチューナー (その相手を全部切って取り上げる)。同じチャンネルの選局を選んだときは選局し直さずに共有する。組の中に使えるチューナーが無ければ次の組へ。どの組にも無ければ 409
4. **`after` の利用者がいるチューナーを選んだら**、その利用者を正常に終わらせて (HTTP の本文をきれいに閉じる) から選局し直す。同じチャンネルなら終わらせず共有する。選ばなかったチューナーの `after` の利用者には触らない
5. 1 回の選局の利用者は、終わるまで同じチューナーにいる。途中で移さない

「使ってよいチューナー」は、選局が無いもの、余韻 (誰も読まなくなってから 5 秒) で残っているだけのもの、自分より優先度の低い利用者しかいないものです。同じ優先度の相手は取り上げません。

denpa の `tests/fake/tuner-scenes.ts` の場面が、この決まりの受け入れテストです ([17](#17-テスト))。

### 8.4 選局の寿命

- **利用者が切るまで終わりません。** 受信が止まった・機材が抜かれた・取り上げられた、のどれかで終わるのは失敗です
- **取り上げた相手の接続は壊します** (TCP を RST で閉じる。Go なら `http.ResponseController` で `Hijack` して `SetLinger(0)` で `Close`、または `panic(http.ErrAbortHandler)`)。きれいに閉じると denpa は録り終えたと読みます
- 壊すのは 1 バイトでも送ったあとだけ。送る前に落ちたら JSON の理由を返します ([7.7](#77-選局の失敗と理由))
- 引き継ぎ (`after`) で終わらせる利用者だけは正常に閉じます
- **余韻。** 利用者が 0 人になってから 5 秒は選局を残します (連続する録画で同じチャンネルを取り直さないため)。5 秒たったら受信を止め、選局を消します。チューナーは開いたままです
- **チューナーを手放すとき** (定義から消した・無効にした・抜かれた・エージェントを止める) にチューナーを閉じます。閉じる前に受信を止め、給電を 0 にします
- **最初の TS を 30 秒待ちます。** 届かなければその選局を失敗で畳みます
- **利用者が読むのが遅いときは、その利用者のぶんを 64MB まで溜め、超えたらその利用者だけ切ります** (理由 `読み出しが追いつかないため切断しました`)。録画と視聴で分けません
- 復号は読み出しとは別の goroutine で行い、間に 64MB の列を置きます。カードが固まっても読み出しを止めません

### 8.5 同期しなかったら同じ組の別のチューナーで選局し直す

- ロックしなかったら、同じ組 (受けられる種別がまったく同じ) の、まだ試していない空いているチューナー (余韻だけのものも) で選局し直します。ほかの組へは移りません。優先度の低い利用者がいるチューナーからは取り上げません
- 試すのは 1 回の選局で 2 台までです。どれも駄目なら、試したチューナーの名前を `・` でつないで同じ文言で 500 を返します (`PLEX PX-W3U4 1-2 #2・PLEX PX-W3U4 1-2 #3: 同期しませんでした (…)`)
- 名指し (`tuner`) と `scan` の利用者は選局し直しません
- 同期しなかったチューナーは、そのチャンネルで 1 時間、組の後ろに回します。同期すれば消します。定義を書き換えたときも消します
- 選局し直したことと、し直しても駄目だったことは記録に 1 行ずつ出します

### 8.6 共有

- 1 回の選局に利用者を何人でもぶら下げます。復号は選局につき 1 回で、復号した TS を人数ぶん配ります
- 選局の途中で共有に入った利用者も、最初の TS が届くまでレスポンスヘッダを返しません。選局が失敗したら、その利用者にも同じ理由を返します
- 鍵の要らない利用者 (`use` の先頭が `epg` `logo` `scan`) しかいない選局では、カードに問い合わせず、溜めずにそのまま流します。あとから鍵の要る利用者が入ったら、選局の始めと同じように溜め直して最初の鍵から復号します ([9.3](#93-復号の決まり))

## 9. B-CAS と復号

### 9.1 カードリーダー

**カードへの経路は OS ごとに違いますが、エージェントの中では `CardLink` (APDU を送って応答を受ける) の 1 つのインターフェースにそろえます。** リーダーの一覧も同じ形 (名前・内蔵か外付けか) です。

| OS      | 内蔵リーダー                                                                                  | 外付けの USB リーダー                                 |
| ------- | --------------------------------------------------------------------------------------------- | ----------------------------------------------------- |
| Linux   | pcscd (px4-drv-pcsc の `libifd-px4.so` が出す)。名前は `PLEX PX-MLT5PE Smart Card Reader [ID: …] (…) 00 00` のような形 | pcscd (libccid)                                        |
| Windows | DriverHost_PX4 のカードのパイプ (`LIST_READERS`・`CONNECT`・`TRANSMIT`)。名前は `PLEX PX-MLT5PE Smart Card Reader (ID: …)` | System32 の `winscard.dll` (`SCardListReadersW` など)。WinSCard_PX4 の互換 DLL は使わない (エージェントの隣に置かない) |
| macOS   | DriverHost_PX4 のカードのパイプ。PLEX の内蔵リーダーは PCSC.framework には出ない              | PCSC.framework (`SCardListReaders`)                   |

Linux の pcscd には、pcsc-lite の socket (`/run/pcscd/pcscd.comm`。`PCSCLITE_CSOCK_NAME` で変えられる) に直接つなぐ pure Go のクライアントを書きます (cgo を使わず、静的なバイナリで distroless に置けるように)。プロトコルは pcsc-lite の `winscard_msg.h` のとおり (バージョン 4.4)。使うのは `ESTABLISH_CONTEXT`・`LIST_READERS`・`CONNECT`・`TRANSMIT`・`DISCONNECT`・`GET_STATUS_CHANGE` だけです。pure Go で間に合わなければ cgo で `libpcsclite` を使ってもよく、その場合はイメージを distroless から変えます。Windows の `winscard.dll` は `golang.org/x/sys/windows` の `LazyDLL` で呼び、macOS の PCSC.framework は cgo で呼びます (macOS のビルドは cgo あり)。

共通の決まり。

- リーダーの名前は、取ってきたものをそのまま使います。画面の表とログにそのまま出します
- **排他で開きます** (PC/SC は `SCARD_SHARE_EXCLUSIVE`、カードのパイプは `EXCLUSIVE`、`T=1`)。開けなければ理由 (共有違反) を返し、少し置いて探し直します
- 起動したときにカードを開いておきます (最初の選局がカードを開くのを待たない)。`CARD_URL` のときは手元のカードを使いません
- 見つかったリーダーを内蔵 → 外付けの順に試し、**カードが挿さっていて INT に応答した最初の 1 つ**で開きます。使わない設定のリーダー (`cards.json`) は飛ばします
- リーダーが切れたら (カードの抜去・USB の抜去・socket やパイプが切れた)、探し直して INT からやり直します。繋ぎ直しに失敗したら 10 秒は探し直さず、同じ理由で断ります
- pcscd・DriverHost_PX4・OS の PC/SC に繋がらなければ、`/denpa/card` に `ok: false` と理由 (`pcscd に繋がりません (<socket>)`、`DriverHost_PX4 に繋がりません (<パイプ>)`) を返します
- 内蔵リーダーをカードのパイプで使うときは、`STATUS` を 5 秒ごとに送ってカードの有無を見ます (pcscd の `GET_STATUS_CHANGE` に当たるものが無い)

### 9.2 カードへのコマンド

APDU は 5 つです。CLA は `0x90`、応答の返り値は応答の 5〜6 バイト目 (BE16)。

| コマンド | INS    | 本文                                     | 応答の読み方                                                                                                                                         |
| -------- | ------ | ---------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------- |
| INT      | `0x30` | 無し (`90 30 00 00 00`)                  | 57 バイト以上。返り値 `0x2100` でなければ B-CAS ではない。CA_system_id は 7〜8 バイト目 (BE16)、system key は 17〜48 バイト目 (32)、CBC の初期値は 49〜56 バイト目 (8) |
| IDI      | `0x32` | 無し                                     | 7 バイト目が番号の数、その後ろに 10 バイトずつ。1 つの 10 バイトの 3〜8 バイト目の 48 ビットを符号なしの数として読む。画面には 10 進 16 桁で出す |
| ECM      | `0x34` | `90 34 00 00 <Lc> <ECM の中身> 00`       | 25 バイト以上。奇数鍵 7〜14 バイト目、偶数鍵 15〜22 バイト目、返り値 5〜6 バイト目。契約が無いなどで断られたときは例外にせず返り値で返す            |
| EMM      | `0x36` | `90 36 00 00 <Lc> <ペイロード> 00`       | 返り値だけ。`0x2100` で受け入れ                                                                                                                       |
| 契約確認 | `0x3C` | `90 3C 00 00 <Lc> <MJD BE16><verification> 00` | 返り値だけ (`0x0800` なら月額の契約あり など)。denpa の `POST /denpa/card/confirm` が使う                                                      |

- ECM の中身は、セクションの先頭 8 バイトと末尾の CRC を除いた部分です (libaribb25 と同じ)。長さは 1〜255 バイト
- カードへは同時に 1 つだけ送ります (ECM・EMM・契約確認で 1 つのロック)
- 同じ ECM には同じ鍵を返します (中身をキーにして数秒だけ控える)
- カードの番号は libaribb25 と同じ読み方で、画面の表 (`ids`) に出ます
- **A-CAS カード (B-CAS 互換モード)**: 実機の A-CAS カード (ATR `3B F0 13 00 FF 91 81 B1 FE 46 1F 03 19`) は、B-CAS の INT (`90 30 00 00 00`) を `6A 86` の 2 バイトで断ります。開くときは、先に B-CAS の INT を送り、通らなければ P2 を変えた INT を送り直します。通れば A-CAS として扱い、それ以降のコマンドは P2 だけを変えます (CLA・INS・本文・応答の読み方は B-CAS と同じ)。値は実機で確かめました (INT の P2=2 が 57 バイトを返す)。`card` の種類は互換で動くので `B-CAS` と返します
  - INT: P2 = `02`、IDI: P2 = `01`、ECM: P2 = `02`、EMM: P2 = `01`。契約確認 (`0x3C`) は未確認で、P2 は 0 のまま
  - 4K の MMT/TLV の復号 (B61) は含みません

### 9.3 復号の決まり

ARIB STD-B25 の MULTI2 (ラウンド 4、system key と CBC の初期値は INT の応答) で ES を復号します。**挙動は denpa の付属のエージェント (`agent/Denpa.Agent/B25.cs`) と libaribb25 に揃えます。**

- 読むのは PAT (PID 0)・PMT・ECM・CAT (PID 1)・EMM だけ。NIT・SDT・EIT は読まない
- PAT → PMT → CA 記述子 (`0x09`) で ES と ECM の PID を結ぶ。CA 記述子はカードの CA_system_id (B-CAS は `0x0005`) のものだけ使う
- ストリームの順に同期で鍵を受け取る。ECM の中身が変わったらその場でカードに問い合わせ、以降のパケットをその鍵で復号する。待つ間は次のパケットへ進まない
- 受け取れなかったら鍵を捨ててスクランブルされたまま流し、同じ中身は 2 秒ぶん流れてから問い合わせ直す。契約が無いと答えた ECM の ES もそのまま流し、「復号できなかったパケットの数」には入れない
- 鍵の要る利用者 (録画・ライブ) がいる選局の先頭は、PAT・PMT と全部の ECM の最初の応答がそろうまで (上限 8MB) 溜め、最初の ECM を挟んで続いている偶奇の並びだけを最初の鍵で復号する
- 来ない PMT は、PAT が変わってから同じ PMT がもう一度来たら来ないとみなす。ただし、どの PMT も挙げていない PID のスクランブルされたパケットが来ている間は待ち続ける
- `service` (観る局の service_id) を付けた利用者だけの選局は、その局の PMT と ECM の答えがそろった時点で流し始める。まだそろっていない局のスクランブルされたパケットは捨てる (8MB まで)。録画 (`service` 無し) が 1 人でもいれば全部の局を待つ
- 溜めている間は PAT・PMT を待たずに ECM のセクションを取っておき、PMT が挙げた ECM をその場でカードに問い合わせる。PMT が挙げていない ECM はカードに送らない
- 映像・音声の PES の先頭では、復号した中身が `00 00 01` で始まるかで鍵が合っているかを確かめる。合わなければ記録に出し、その PES はスクランブルされたまま流す。stream_type `0x06` は確かめない
- 偶奇は STD-B25 のとおり (`transport_scrambling_control` の `0b10` が偶数、`0b11` が奇数)
- 観た局の PAT・PMT はメモリに覚え、同じ局のライブで本物を待たずに流し始めてよい (denpa の `PsiMemory` と同じ)。最初の実装では省いてよく、省くときは README に書く
- 番組が替わって PMT の並びが変わっても、復号の経路を組み直すだけで選局は畳まない (畳むのは denpa 側)

### 9.4 EMM

- CAT の CA 記述子からカードの方式の EMM の PID を取り、EMM セクション (`table_id 0x84`) を組み立てる
- セクションの各ペイロード (先頭 6 バイトがカード ID) をカードの番号 (個別とグループ) と比べ、合ったものだけをカードに渡す (ペイロード 13〜255 バイト)
- 比べるのは復号の goroutine、カードに送るのは別の goroutine 1 本。順番待ち 16 件、1 秒に 1 件まで
- 応答したもの (`0x2100`・`0xA102`・`0xA107`) は 256 件まで控えてもう送らない。通信できなかったものは控えない
- 受け入れたら (`0x2100`) 控えている ECM の応答を捨て、`card` の通知を出す (1 分に 1 回まで)
- 番組表・ロゴ・スキャンの選局でも渡す。`/denpa/decode` と `--tune` からは渡さない
- `CARD_URL` のときは配る側の `/denpa/card/emm` に渡す。404 なら記録に 1 回出して、以後は送らない

### 9.5 鍵を配る・貰う

denpa の付属のエージェントと同じ wire です。配る側 (`CARD_SHARE_PORT`) と貰う側 (`CARD_URL`) の両方を持ち、付属のエージェントとも相互に通じます。

配る側は平文の HTTP で、鍵を配る API にだけ応答します (ほかは 404)。相手は LAN の中に絞ります。

| エンドポイント              | 本文 (バイト列)                          | 応答 (バイト列)                                                                 |
| --------------------------- | ---------------------------------------- | ------------------------------------------------------------------------------- |
| `GET /denpa/card/init`      | 無し                                     | system key 32 + CBC の初期値 8 + CA_system_id (BE32) + カード番号 (BE64 の並び)   |
| `POST /denpa/card/ecm`      | ECM の中身 (1〜255 バイト)               | 奇数鍵 8 + 偶数鍵 8 + 返り値 (BE16)                                             |
| `POST /denpa/card/confirm`  | MJD (BE16) + verification (1〜253 バイト) | 返り値 (BE16)                                                                   |
| `POST /denpa/card/emm`      | ペイロード (13〜255 バイト)              | 返り値 (BE16)                                                                   |

失敗は JSON (`{"error": "…"}`) で、本文が長すぎれば 400、範囲の外なら 403 です。付属のエージェントの `Respond.Error` と同じ形で、`"ok"` は付けません。貰う側は `"error"` の文字列だけを読みます。

絞り方は 3 つです (値と上限は denpa の `docs/agent.md`「鍵を配るのは LAN の中だけ」と同じ)。

1. `CARD_SHARE_PORT` を書いたときだけ、そのポートで、全部のインターフェースで待つ (`AGENT_PORT` と同じ値は起動を止める)
2. 相手のアドレスが `CARD_SHARE_NETWORKS` (既定 `127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,::1/128,fc00::/7,fe80::/10`) の中。IPv4 射影アドレスは IPv4 として見る。既定の範囲からはみ出す値 (`100.64.0.0/10`、グローバル、`0.0.0.0/0`) が 1 つでもあれば起動しない
3. TTL (IPv6 は hop limit) を `CARD_TTL` (既定 1、上限 8) にする。待ち受けと受けた接続の両方に付け、IPv4 と IPv6 は別のソケットで待つ。貰う側も繋ぐ前に付ける
4. `CARD_SHARE_MAX_RTT` (ミリ秒) を書いたときは、OS が TCP から測った往復時間 (Linux `TCP_INFO`、macOS `TCP_CONNECTION_INFO`、Windows `SIO_TCP_INFO`) が超える相手に配らない

貰う側 (`CARD_URL`) は、起動時と ECM のたびに上の API を呼び、応答を 10 秒まで待ちます。`/denpa/card` には `source: "remote"` と `remote: <URL>` と配る側のカード番号を返します。

### 9.6 カードの状態 (`GET /denpa/card`)

```json
{
  "ok": true,
  "message": "",
  "source": "local",
  "readers": [
    { "name": "PLEX PX-MLT5PE Smart Card Reader [ID: …] (…) 00 00", "card": "B-CAS", "ids": ["0000123456789012"], "active": true, "tuners": ["PLEX PX-MLT5PE 4-12.1 #0"], "disabled": false }
  ]
}
```

- `message` は問題があるときだけ (`pcscd に繋がりません (…)`、`カードが 10 秒以内に応答しません`、`使うカードリーダーがありません。カードリーダーをオンにしてください`)
- `readers` は見つかったリーダー全部。`card` はカードの種類 (B-CAS なら `B-CAS`、無ければ null)、`ids` はカードの番号 (読めなければ空)、`active` はいま使っているか、`tuners` は使っているなら復号中のチューナーの名前、`disabled` は使わない設定か。開けなかったリーダーは `error` に理由
- `CARD_URL` のときは `source: "remote"`、`remote: "<URL>"`、`ids`、`tuners` を外に出し、`readers` は空
- **denpa は 10 秒で待つのをやめる**ので、9 秒で切り上げて理由を返します。確認は 1 つずつ (同時に来ても 1 回の確認を使い回す)

`PUT /denpa/card/readers` (`{"disabled": ["<リーダー名>", …]}`) は使わないリーダーを `cards.json` に書き、使っている最中なら手放して次のリーダーで繋ぎ直します。挿さっていないリーダーの「使わない」は残します。応答は `{"disabled": [...]}` (並びは文字列の順)。

`POST /denpa/card/confirm` (`{"verification": "<Base64>", "date": <MJD>}`) は契約確認の返り値を `{"ok": true, "code": 2048}` で返します。`CARD_URL` のときは配る側に渡します。

### 9.7 後から復号する (`POST /denpa/decode`)

`{"input": "<置き場からの相対パス>", "output": "<同>"}` を受け、スクランブルされたまま録れた TS を復号して書きます。

- 置き場は `RAW_DIR` (無ければ `MEDIA_DIR/raw`、どちらも無ければ Linux は `/media/raw`、Windows と macOS は `$DENPA_HOME/recorded`)。置き場の外・リンク・`..` は 400
- 書き出す先がもうあれば断る (`{"ok": false, "error": "…"}`、500)。失敗したら自分で作った書きかけを消す
- 結果は `{"ok": true, "packets": <数>, "scrambled": <復号できなかった数>}` のような JSON。denpa が読むのは `ok` と `error` だけなので、ほかの項目は付属のエージェントの `Scramble.Decode` に合わせる
- EMM は渡さない

`POST /denpa/raw/probe` (`{"path": ".denpa/probe-…"}`) は、置き場の `.denpa/` の下の 4 KiB までのファイルの中身の SHA-256 を `{"ok": true, "sha256": "<hex>"}` で返します。ほかのパスは 400、無ければ 404。

## 10. denpa との API

**HTTPS (`AGENT_PORT`、既定 25252)、HTTP/1.1 だけ。** `h2` は使いません。組んだ denpa しか通しません ([11](#11-denpa-と-tls-で組む))。応答の JSON は UTF-8 で、日本語をエスケープしません。

| メソッドとパス               | 何                                                                                                     |
| ---------------------------- | ------------------------------------------------------------------------------------------------------ |
| `GET /denpa/stream`          | 選局して TS を流す ([10.1](#101-get-denpastream))                                                        |
| `GET /denpa/events`          | 通知 (SSE) ([10.2](#102-get-denpaevents))                                                                |
| `GET /denpa/tuners`          | チューナーの一覧 ([10.3](#103-get-denpatuners))                                                          |
| `PUT /denpa/tuners`          | 定義を書き換える ([10.4](#104-put-denpatuners))                                                          |
| `GET /denpa/channels`        | 預かっているチャンネル (`channels.json` の配列をそのまま)                                               |
| `PUT /denpa/channels`        | スキャンの結果を預かる ([10.5](#105-put-denpachannels))                                                  |
| `DELETE /denpa/channels?type=` | 1 つの種別のチャンネルを全部消す。`type` が無ければ 400。応答は残った配列。`channels` の通知を出す     |
| `GET /denpa/card`            | カードの状態 ([9.6](#96-カードの状態-get-denpacard))                                                   |
| `PUT /denpa/card/readers`    | 使わないリーダー ([9.6](#96-カードの状態-get-denpacard))                                               |
| `POST /denpa/card/confirm`   | 契約確認 ([9.6](#96-カードの状態-get-denpacard))                                                       |
| `POST /denpa/decode`         | 後から復号する ([9.7](#97-後から復号する-post-denpadecode))                                            |
| `POST /denpa/raw/probe`      | 置き場が見えるか ([9.7](#97-後から復号する-post-denpadecode))                                          |
| `POST /denpa/pair`           | 組む ([11](#11-denpa-と-tls-で組む))。組む前の相手からも受ける                                          |
| `DELETE /denpa/pair`         | 呼んだ denpa の登録を消す。204                                                                          |
| `GET /denpa/logs`            | 直近のログ (`{"lines": [{"at": <ミリ秒>, "text": "…"}]}`。古い順に最大 2000 行。メモリだけ)             |
| `GET /denpa/about`           | `{"name": "<AGENT_NAME か機械の名前>", "version": "<DENPA_VERSION か dev>", "os": "linux|windows|macos", "arch": "x64|arm64", "runtime": "<OS の説明>", "ccidDriver": null}` |
| それ以外                     | 404 `{"ok": false, "error": "not found"}`                                                              |

エラーの本文は `{"ok": false, "error": "<理由>"}` です。denpa は `error` を人に見せます。

### 10.1 `GET /denpa/stream`

| クエリ     | 必須 | 意味                                                                                           |
| ---------- | ---- | ---------------------------------------------------------------------------------------------- |
| `type`     | 要   | 種別                                                                                           |
| `channel`  | 要   | チャンネル名                                                                                   |
| `priority` | 任意 | 優先度 (整数。無ければ 0)                                                                      |
| `use`      | 任意 | 利用者の名乗り (`rec 12`・`live GR/T16`・`epg BS15:16625`・`logo GR/T16`・`scan T21`)。無ければ `不明` |
| `after`    | 任意 (複数) | 引き継ぐ利用者の `use`                                                                  |
| `tuner`    | 任意 | 名指し。チューナーの `pin` (`device`、無ければ `name`)                                         |
| `service`  | 任意 | 観る局の service_id (1〜65535)。読めなければ 400 `service は 1〜65535 の番組番号です`           |

- 応答の本文は素の TS (TLV のときは TLV) を chunked で流します。何も包まず、届いた塊ごとに flush します
- **レスポンスヘッダは最初の TS を書くときに送ります。** `Content-Type: video/MP2T` (TLV なら `application/x-arib-tlv`)。ISDB-C で有効な TS の数が分かるときだけ `X-Denpa-Tsmf-Streams: <数>` ([7.6](#76-tsmf-の有効な-ts-の数-x-denpa-tsmf-streams))
- 送り出しの応答バッファは使いません (Go の `http.ResponseWriter` は `Flush` を塊ごとに呼ぶ)
- 利用者が読むのをやめたら (`Request.Context` の Done)、その利用者を選局から外します。利用者が 0 人なら余韻に入ります
- 覚えた PAT・PMT で流し始めるときは、先頭に PAT と観る局の PMT のパケットを 1 組差し込みます (実装するなら)

### 10.2 `GET /denpa/events`

`text/event-stream`。繋いだ時点でヘッダを送り、通知が無い間も 30 秒ごとに `: keep-alive\n\n` を流します (denpa は 5 分何も届かない接続を切るため)。

通知は `event: <名前>\ndata: {}\n\n` で、名前は 3 つです。

| 名前       | いつ                                                                                      |
| ---------- | ----------------------------------------------------------------------------------------- |
| `tuners`   | 選局が始まった・終わった・利用者が増減した・定義が変わった・機材が増減した                 |
| `channels` | `PUT`・`DELETE /denpa/channels` で預かりものが変わった                                    |
| `card`     | カードが EMM を受け入れた (1 分に 1 回まで)                                               |

購読者ごとの列は 64 件で、溜まったら古いほうを捨てます。止まるときはこちらから閉じます。

### 10.3 `GET /denpa/tuners`

```json
{
  "tuners": [
    {
      "index": 0,
      "rank": 0,
      "name": "PLEX PX-W3U4 1-2 #2",
      "types": ["GR"],
      "receivable": ["GR"],
      "disabled": false,
      "device": "px4:/dev/px4video2",
      "pin": "px4:/dev/px4video2",
      "lnb": null,
      "channel": { "type": "GR", "channel": "T27" },
      "users": [{ "use": "rec 12", "priority": 10 }],
      "error": null,
      "signal": { "locked": true, "cn": 27.3, "unit": "dB", "atTune": false, "strengthDbm": null }
    }
  ],
  "detected": true
}
```

- `rank` は [8.2](#82-選ぶ順-rank)。無効なら null
- `channel` は空いていれば null。余韻の間は入ったまま (利用者は空)
- `error` は、選局の失敗が残っていればその理由、無ければ機材の注意書き、無ければ null
- `signal` は使用中で測れるときだけ
- `detected` は定義 (`tuners.json`) が無くて自分で見つけた状態か
- Windows と macOS では `device` が `px4host:…` です

### 10.4 `PUT /denpa/tuners`

`{"tuners": [{"name": "…", "types": ["GR"], "device": "px4:/dev/px4video2", "lnb": "15v", "disabled": false}, …]}`。

- `name` の無い項目があれば 400 `name の無いチューナーがあります`。型が違えば 400 (`<名前>: types は文字列の配列にします` など)
- 空の配列は定義を消す (自動検出に戻す)
- 書き換えは、選局と読み出しが止まるのを待ってから行う。名前が同じチューナーは選局を保ったまま定義だけ替え、無効にしたチューナーの選局は畳んで利用者を残さない
- 応答は `GET /denpa/tuners` と同じ
- 選局コマンドのような自由な文字列は受け取らない。`device` は `px4:`・`px4host:`・`cmd:` で始まるものだけ受け、それ以外は 400。`cmd:` の `<model>` は機材の定義にある `command` 付きの機種に限る (無ければ 400)。`command` の引数の一部に `{device}` が入る機種では、`device` の末尾はホスト名か IPv4 だけにする (それ以外は 400)

### 10.5 `PUT /denpa/channels`

`{"channels": [<項目>, …], "scanned": ["GR", …]}`。項目の中身は denpa が決め (`type`・`channel`・`networkId`・`transportStreamId`・`remoteControlKeyId`・`transport`・`services`)、エージェントは解釈せずに保存します。

- `channels` と `scanned` が無い・`scanned` が空 → 400 `channels と scanned が必要です`
- `channels` に 1 件も無い → 400 `チャンネルが 1 件もありません` (今まで録れていた局を消さない)
- `scanned` と、各項目の `type`・`channel` が文字列でない → 400 `scanned と、channels の type・channel は文字列で書きます`
- `scanned` にある種別の項目だけを入れ替え、ほかの種別は残す。種別の順 (`GR` `BS` `CS` `CATV` `SKY`)、次にチャンネル名の順に並べて書く
- 書くときは `<ファイル>.writing` に書いてから rename する
- 応答は入れ替えたあとの配列。`channels` の通知を出す

## 11. denpa と TLS で組む

**denpa の付属のエージェントの `AgentTls.cs` と同じ手順です** (denpa-mirakurun-bridge の `tls.go` が同じものを Go に移植しています。MPL-2.0 のファイルとしてそのまま取り込んでよい)。

- 自分の鍵 (EC P-256) と自己署名の証明書 (100 年) を `tls/agent.key`・`tls/agent.crt` に持つ。無ければ作り、作り直さない
- TLS 1.2 と 1.3。クライアント証明書を必須にし (`tls.RequireAnyClientCert`)、TLS の段ではどの証明書も受ける
- 相手の証明書の指紋 (DER の SHA-256、小文字 16 進 64 字) が `tls/clients` (1 行 1 つ) にあれば通す。自分の証明書の指紋も通す (`--status` 用)。無ければ `POST /denpa/pair` だけ受け、ほかは 403 `{"ok": false, "error": "この denpa とはまだ組んでいません。…", "paired": false}`
- 合言葉は `AGENT_PAIR_TOKEN`、無ければ `tls/pair-token` (32 バイトの乱数を base64url で 43 字。無ければ作る)。32 字未満なら記録に注意を出す
- `POST /denpa/pair` の本文は `{"client": "<D>", "proof": "<base64url>"}`、1024 バイトまで (超えたら 413 `本文が大きすぎます (上限 1024 バイト)`)。`client` が接続の証明書の指紋と違えば 403 `証明書が名乗りと違います`。`proof` が `base64url(HMAC-SHA256(合言葉, "denpa-pair/client\n" + A + "\n" + D))` (A はエージェント、D は denpa の指紋) と違えば 403 `合言葉が違います`。合えば D を `clients` に足し、`{"proof": base64url(HMAC-SHA256(合言葉, "denpa-pair/agent\n" + A + "\n" + D))}` を返す。組んだ相手から来ても受ける
- `DELETE /denpa/pair` は呼んだ相手の指紋を `clients` から消して 204
- `tls/` のファイルは 0600 (Windows は置き場のフォルダの権限のまま)
- まだ誰とも組んでいなければ、起動のときに標準出力に `[agent] denpa と組むときの URL: https://<機械の名前>:<ポート>#pair=<合言葉>` を出す。`/denpa/logs` には載せない
- Host ヘッダと Content-Type は見ない

## 12. 設定・環境変数・ファイル

### 12.1 環境変数

| 変数                                                                          | 既定                                                           | 何                                                                                                 |
| ----------------------------------------------------------------------------- | -------------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| `AGENT_PORT`                                                                  | `25252`                                                        | 待ち受けるポート。1〜65535 でなければ起動しない                                                     |
| `AGENT_HOST`                                                                  | 全部のインターフェース                                         | 待ち受けるアドレス (IP)。IP でなければ起動しない                                                    |
| `AGENT_PAIR_TOKEN`                                                            | (空。`tls/pair-token` に作る)                                  | 合言葉                                                                                             |
| `AGENT_NAME`                                                                  | (空。機械の名前)                                               | `/denpa/about` の `name`                                                                           |
| `DENPA_HOME`                                                                  | `~/denpa`                                                      | Windows と macOS の置き場の親 (`config/`・`recorded/`)。Linux では使わない                           |
| `TUNERS_FILE` / `CHANNELS_FILE` / `DEVICES_FILE`                              | Linux `/config/…`、Windows と macOS `$DENPA_HOME/config/…`     | `tuners.json`・`channels.json`・`devices.json`。`cards.json` と `tls/` は `tuners.json` の隣         |
| `MEDIA_DIR` / `RAW_DIR`                                                       | Linux `/media` / `$MEDIA_DIR/raw`、ほかは `$DENPA_HOME/recorded` | 元の TS の置き場                                                                                   |
| `SHUTDOWN_WAIT`                                                               | `21600000` (6 時間)                                            | 止められたとき、録画が終わるまで待つ上限 (ミリ秒)。`0` で待たない                                  |
| `DENPA_VERSION`                                                               | `dev`                                                          | `/denpa/about` の `version`。denpa と同じ値にすると画面に「バージョンが違います」が出ない          |
| `CARD_URL`                                                                    | (空)                                                           | 鍵を貰う先 (配る側の `CARD_SHARE_PORT`)。書くと手元のカードを使わない                              |
| `CARD_SHARE_PORT` / `CARD_SHARE_NETWORKS` / `CARD_TTL` / `CARD_SHARE_MAX_RTT` | 配らない                                                       | [9.5](#95-鍵を配る貰う)                                                                            |
| `PCSCLITE_CSOCK_NAME`                                                         | `/run/pcscd/pcscd.comm`                                        | Linux の pcscd の socket                                                                           |
| `PX4_RUNTIME_DIR`                                                             | `/tmp`                                                         | macOS の DriverHost_PX4 の socket の置き場                                                         |
| `DRIVER_HOST_PATH`                                                            | 実行ファイルの隣の `DriverHost_PX4.exe`                        | Windows で DriverHost_PX4 を起動するときのパス                                                     |
| `DRIVER_HOST_INI`                                                             | `DRIVER_HOST_PATH` の隣の `DriverHost_PX4.ini`                 | Windows と macOS で、機材の定義 (`DeviceInterfaceGUID`・`VendorId`・`ProductId`・受信機) を読む元   |
| `FAKE_TUNE`                                                                   | (空)                                                           | 偽の選局 (適合テスト)。`cmd` バックエンドの組み込みの機種 `fake` として扱う ([6.3](#63-選局の流れ)、[17](#17-テスト)) |

読めない値は既定に戻さず、理由を出して終了コード 2 で止まります。

### 12.2 ファイル

| ファイル        | 中身                                                             | 誰が書く       |
| --------------- | ---------------------------------------------------------------- | -------------- |
| `tuners.json`   | `{"tuners": [<TunerSpec>, …]}`                                   | denpa (画面)   |
| `channels.json` | チャンネルの配列                                                 | denpa (スキャン) |
| `devices.json`  | 機材の定義 ([3](#3-機材の定義))                                  | 人             |
| `cards.json`    | `{"disabled": ["<リーダー名>", …]}`                              | denpa (画面)   |
| `tls/`          | `agent.key`・`agent.crt`・`pair-token`・`clients`                | エージェント   |

どれも JSON で、書くときは `.writing` に書いてから rename します。読めない・形式が違うファイルは、記録に出して無いものとして続けます (起動は止めない)。1 台ぶん・1 機種ぶんの書き損じは、その項目だけ飛ばします。

## 13. 止まり方

- 1 回目の止まれの合図 (Linux と macOS は `SIGTERM`・`SIGINT`・`SIGQUIT`、Windows は Ctrl+C と `CTRL_CLOSE_EVENT`・サービスの停止) では、`rec` の利用者がいる間は `SHUTDOWN_WAIT` まで待ちます。待っている間も新しい要求 (録画の選局も) を受けます。録画が無ければすぐ止まります
- 待っている間にもう一度合図が来たら、待たずに止まります
- 止まるときは、通知の接続をこちらから閉じ、選局を全部離し (利用者の接続を壊す)、チューナーを閉じ (chardev: `PTX_STOP_STREAMING` → `PTX_DISABLE_LNB_POWER` → close。host: `SET_CAPTURE(false)` → `SET_LNB_VOLTAGE(0)` → `CLOSE` → パイプを閉じる)、カードを手放して終わります
- Windows で自分が起動した DriverHost_PX4 は止めません (ほかのアプリも使う)
- 捕まえていない panic で落ちるときも、チューナーは閉じてから終わります (5 秒まで待つ)

## 14. ログ

標準出力と `GET /denpa/logs` の両方に出します。1 行は `[agent] <本文>` で、`/denpa/logs` には `[agent] ` を除いた本文を入れます。Windows では標準出力を UTF-8 にします。

チューナーと機材の行は、本文の先頭の `[ ]` に出したものの名前を書きます。

| 出したもの     | `[ ]` の中                                       | 例                                                                           |
| -------------- | ------------------------------------------------ | ---------------------------------------------------------------------------- |
| チューナー     | 設定のチューナーの名前                           | `[PLEX PX-W3U4 1-2 #2] T27: 同期しなかったので PLEX PX-W3U4 1-2 #3 で選局し直します` |
| 機材           | 機種名とポート                                   | `[PLEX PX-W3U4 1-2] 機材が抜かれました`                                      |
| カードリーダー | リーダーの名前                                   | `[PLEX PX-MLT5PE Smart Card Reader [ID: …] 00 00] カードを開きました`          |
| pcscd・DriverHost_PX4 | `pcscd`・`DriverHost_PX4`                 | `[DriverHost_PX4] 繋がりません (\\.\pipe\px4_ctrl_pipe)`                     |

出す行の目安 (denpa の付属のエージェントと同じ場面)。

- 起動: 見つけた機材の一覧、定義の有無、組むための URL (組んでいないとき)、`devices.json` で足した・上書きした機種
- 選局: 失敗の理由、選局し直し、取り上げ (誰が誰を)、最初の TS までの時間、30 秒待っても届かなかった
- 読み出し: 連続性の飛びの回数を 1 分に 1 回 (そのときの利用者を添える)。読み出しが追いつかず切った利用者
- 復号: 鍵が合わなかった PES、復号できなかったパケットの数 (選局が終わったとき)、EMM を受け入れた
- カード: 開いた・切れた・探し直した・断られた理由
- 設定: 定義とチャンネルを保存した・消した

denpa はログの文言を読みません (重さを文字から推し量るだけ)。

## 15. コマンドライン

| 引数                                                  | 何                                                                                                                        |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| (無し)                                                | サーバとして動く                                                                                                          |
| `--version`                                           | バージョンを出して終わる                                                                                                  |
| `--pair`                                              | 組むときの URL を標準出力に出して終わる (鍵と合言葉が無ければ作る。メッセージは標準エラー)                                 |
| `--status`                                            | 動いているエージェントに自分の証明書で繋ぎ、`/denpa/tuners` の JSON をそのまま出す。繋がらなければ理由を出して 1          |
| `--devices`                                           | 機材の定義 (組み込み + `devices.json` + `DRIVER_HOST_INI`) と、いま挿さっている機材を並べて終わる                           |
| `--tune <device[+device…]> <channel[,channel…]> [--lnb 15v] [--decode] [--card-url <url>] [--seconds N]` | サーバを立てずに選局して TS を標準出力へ。実機で確かめる入口。`--decode` で復号も |
| `--decode-file <in.ts> [<out.ts>]`                    | ファイルを復号する                                                                                                        |
| `--card`                                              | リーダーを並べ、カードを開いて INT と IDI の結果を出す                                                                     |

対応していない引数ではサーバを立てず、使い方を出して 2 で終わります。

## 16. 配布

- Go 1.23 以上。`CGO_ENABLED=0` で静的にビルドする (macOS も。今の実装は DriverHost_PX4 への UNIX ドメインソケットだけで、cgo を使わない)。`linux/amd64`・`linux/arm64`・`windows/amd64`・`darwin/arm64`・`darwin/amd64` の 1 ファイルずつ
- nasne 向けに `linux/mipsle` (`GOMIPS=softfloat`) も出す ([6.5](#65-sony-nasne-sonynasne-debian-の-nasne-recpt1) の置き方 B)。`cmd` バックエンドだけで、pcscd と chardev は使わない構成なので、リリースの表では「試験的」と書く
- **nasne 向けのバイナリは、ChaCha20-Poly1305 の MIPSLE のアセンブリ入りの専用ビルド**にする (`scripts/build-nasne.sh`。取り込んだアセンブリ `third_party/x-crypto-mipsle` を、専用に複製した GOROOT に当て、`-D=GOMIPS_r2` で `ROTR` を使う)。標準のビルドより、TLS の暗号が約 4.5 倍速く、実放送の TLS でエージェントの CPU が 1 コアの 88〜92% → 42〜43% になる ([6.5](#65-sony-nasne-sonynasne-debian-の-nasne-recpt1)、[#3](https://github.com/maimainoue/denpa-px4-agent/issues/3))。**MIPS32r2 以上の機材だけで動く** (nasne の 74Kc は r2。r1 の機材では、実行時に不正な命令になる)。配るときは、バイナリのとなりの `*.licenses.txt` (アセンブリの MIT と、Go の BSD のライセンス) を付ける。上流 (golang/crypto の PR 294) にマージされたら、この専用ビルドは要らなくなる

### Linux

- Docker イメージは `gcr.io/distroless/static` に置く。`USER` は root のまま (デバイスファイルのグループが `video` で、コンテナの中のユーザーに合わせられないため。要れば `--group-add video` で変える)
- nasne (HTTP) の組み込みは、`curl` を使わずエージェントが HTTP で直接読むので、Docker のイメージでも使える (6.5。[#17](https://github.com/maimainoue/denpa-px4-agent/issues/17))
- denpa の `compose.override.yml` で `tuner-agent` を差し替える。px4_drv のメジャー番号は動的なので、`device_cgroup_rules` は `c *:* rmw` にし、`/dev` をディレクトリごと渡す。`/sys` は読み取りで渡す (機材の見分けに使う)
- pcscd は、ホストの socket (`/run/pcscd`) を渡すか、コンテナの中で動かす。px4_drv の README「Docker 環境からカードリーダーにアクセスする場合」に従う。両方で動かすと競合する

```yaml
# denpa の compose.override.yml
services:
  tuner-agent:
    image: ghcr.io/<owner>/denpa-px4-agent:latest
    cap_add: !reset []
    device_cgroup_rules: !override
      - "c *:* rmw"
    volumes: !override
      - ./config:/config
      - denpa-recorded:/media/raw
      - /dev:/dev
      - /sys:/sys:ro
      - /run/pcscd:/run/pcscd
    stop_grace_period: 6h5m
```

### Windows

- zip で配る。中身は `denpa-px4-agent.exe` と `README`。px4_drv の WinUSB 版 (`DriverHost_PX4.exe`・`DriverHost_PX4.ini`・`it930x-firmware.bin`・機種の inf) は px4_drv の配布物から入れ、`DRIVER_HOST_PATH` で場所を指す (既定は同じフォルダ)
- denpa の `install.ps1` が入れるエージェントの代わりに置く。`denpa.env` (`$DENPA_HOME`) の環境変数を読む
- 常駐はタスクスケジューラかサービス。DriverHost_PX4 を自分で起動するので、同じセッションで動かす

### macOS

- tar.gz で配る。DriverHost_PX4 は px4_drv の macOS 版を launchd で root として先に動かす (`PX4_RUNTIME_DIR` を合わせる)
- denpa の `install.sh` が入れるエージェントの代わりに置く。`denpa.env` の環境変数を読む

## 17. テスト

### 17.1 denpa の適合テスト

**denpa の `agent/conformance.test.ts` をそのまま通します。** `AGENT_CMD` にこのエージェントの実行ファイルを渡して `bun test agent/conformance.test.ts` を回します。

そのために、付属のエージェントと同じ `FAKE_TUNE` を持ちます。`FAKE_TUNE` が書いてあれば、機材を開く代わりに `<FAKE_TUNE> <種別> <チャンネル名>` を子プロセスとして起動し、標準出力を TS として読みます (`cmd` バックエンドの組み込みの機種 `fake`。[6](#6-コマンド-recpt1-互換の-cli-のバックエンド))。非 0 で終わったら「同期しなかった」です。止めるときはプロセスグループごと `SIGTERM` (Windows はジョブオブジェクトで止める。`cmd` バックエンドのうち Windows で動くのはこの `fake` だけ) を送ります。`tuners.json` の `device` が無いチューナーでも動きます。

適合テストが押さえるもの (2026-11 時点): TLS と組む手順、`/denpa/stream` の TS がそのまま流れること、共有、取り上げ、409 と 500、無効なチューナー、SSE、`/denpa/channels` の入れ替えと断り方、ログと `about`、カードの状態、鍵を配る API の場所と絞り方、`/denpa/decode` の断り方、404、定義の書き換えと `rank`、`tests/fake/tuner-scenes.ts` の全場面、止まり方、名指し。

### 17.2 単体テスト (Go)

- チャンネル名 → 選局の値 (chardev と host の両方。表の全部と境界。`BS07`・`BS17`・範囲外を断る)
- `cmd` のチャンネル名の直し方 (`recpt1`・`recisdb`・`px4rec` の表の全部。`:TSID` と ISDB-C を断る組み合わせ)、引数の組み立て (`{lnb}`・`{service}`・`{sidQuery}`・`serviceArg`・`tsidArg` が消える・配列に展開される)、子プロセスの起動と停止 (非 0 で終わる・`busyExitCode` は 409・止まらない子を `SIGKILL`)
- `singleService` の選局の単位 (種別 + チャンネル + 局) と共有の判定、`service` の無い録画を 400 で断ること。`decryptedByDevice` で復号の経路を通らず `/denpa/card` が擬似のリーダーを返すこと
- 機材の定義の重ね方 (組み込み + `devices.json` + `DRIVER_HOST_INI`)。`types` の上書きが `receivable` に出ること
- `rank` の付け方と `Open` の選び方 (`tuner-scenes.ts` の場面を Go のテーブルに写す)
- DriverHost_PX4 のコマンドの大きさと並び (Windows と macOS の定数)。偽のパイプ・socket で OPEN → SET_PARAMS → TUNE → SET_CAPTURE → TS の往復
- MULTI2 の既知の入出力 (ARIB STD-B25 の試験ベクタ) と、デスクランブラに PMT 2 つ・ES ごとの CA・ECM の変化・アダプテーション・ゴミを混ぜた TS を通した結果 (libaribb25 と一致させる)
- EMM の組み立てとカード ID の照合
- 鍵を配る API の wire の往復
- 組む手順の proof
- `PUT /denpa/channels` の入れ替え
- pcscd のクライアント (偽の socket) とカードのパイプのクライアント (偽のサーバ)

### 17.3 実機で確かめる順

1. `--devices` で機材が機種名と受信機つきで並ぶこと
2. `--card` でカードが読めること (内蔵リーダーの名前が出て、番号が出る)
3. `--tune px4:/dev/px4video2 T27 --seconds 10 > t27.ts` (Windows と macOS は `px4host:…`) で TS が出ること。`--decode` で復号できること
4. 衛星 (`--lnb 15v` の要る構成はそれを付けて) `BS15:16625`
5. PX-MLT 系なら `QC30`・`QC30:1`・`H44`
6. サーバとして denpa と組み、チャンネルスキャン → 番組表 → ライブ → 録画。denpa の設定の「受信」で C/N が出ること
7. 録画中に止めて、録画が終わるまで待つこと
8. Windows では TVTest と同時に使い、空いている受信機だけを取ること。macOS では px4rec と同時に使うこと

## 18. ライセンスと参照したもの

- このプロジェクトは GPL-2.0 です (`LICENSE`)
- px4_drv (GPL-2.0) の `include/ptx_ioctl.h` の値、`host/common/command.hpp`・`card_command.hpp` の定義、`docs/isdbc_tuning.md`、`DriverHost_PX4.ini` の GUID を使います。定義は Go に写してよい (同じ GPL-2.0)
- denpa (AGPL-3.0。エージェントは MPL-2.0) は API の約束と挙動の参照だけで、コードは写しません。例外は denpa-mirakurun-bridge の `tls.go` (MPL-2.0。denpa の `AgentTls.cs` の移植) で、取り込むときはファイルの先頭の MPL-2.0 の表示を残します。MPL-2.0 は GPL-2.0 と組み合わせられます (MPL 2.0 の Secondary License)
- libaribb25 (Apache-2.0) と recpt1 (GPL) は、復号の挙動と C/N の式の参照です
- pcsc-lite の wire (`winscard_msg.h`。BSD-3-Clause) を pure Go で書き直します

## 19. 実装の段階

1. **骨組み**: 環境変数・設定ファイル・機材の定義・ログ・HTTPS と組む手順・`/denpa/about`・`/denpa/logs`・`/denpa/tuners` (定義の読み書き)・`/denpa/channels`・SSE。`cmd` バックエンド (`FAKE_TUNE` と recpt1・recisdb・px4rec の直し方) の選局と取り合い。ここで適合テストの TLS・チューナー・通知・チャンネル・診断・定義・止まり方・名指しの節を通す。この段階で、recpt1 が動く機材なら denpa から使える
2. **Linux の chardev**: デバイスの検出、ioctl、`read()`、C/N、抜き差し。`--tune` で実機
3. **カードと復号**: pcscd のクライアント、B-CAS、MULTI2、デスクランブラ、EMM、`/denpa/card`、`/denpa/decode`、`--card`・`--decode-file`。適合テストのカードの節を通す
4. **鍵を配る・貰う**: `CARD_SHARE_PORT`・`CARD_URL`。適合テストの鍵の節を通す
5. **Windows と macOS の host**: パイプと socket、コマンドの構造体、機材の数え方、DriverHost_PX4 の起動、カードのパイプ、OS の PC/SC。`--tune` で実機
6. **ISDB-C と J.83B**: 拡張 ioctl と host の `SET_PARAMS`、TLV、`X-Denpa-Tsmf-Streams` (px4_drv に口を足してから)
7. **配布**: Dockerfile、compose の例、Windows の zip、macOS の tar.gz、README

## 20. 決めていないこと・確かめること

- chardev には `PTX_GET_LOCK_STATUS` が無いので、`locked` を TS の継続で決めている。電波が途切れたときに `locked` が true のまま残らないよう、5 秒 TS が来なければ false にする
- TSMF の有効な TS の数を返す口を px4_drv に足すか ([7.6](#76-tsmf-の有効な-ts-の数-x-denpa-tsmf-streams))
- host で同じ機種が 2 台あるとき、チューナーの名前と筐体が結び付かない ([5.3](#53-機材の見つけ方))。DriverHost_PX4 に受信機を並べるコマンド (USB のシリアル番号つき) を足せば結び付けられる
- pcscd のクライアントを pure Go にするか cgo にするか。pure Go で `TRANSMIT` の構造体 (`SCARD_IO_REQUEST` を含む) の並びを pcsc-lite 1.9 と 2.x で確かめる
- DriverHost_PX4 のコマンドの大きさ (Windows 436・4,952、macOS 820・9,304 など) を、C++ の `sizeof` を出す小さなプログラムで確かめる
- 読み出しの塊の大きさと復号の列 (64MB) で、PX-Q3 系 8 受信機の同時録画が追いつくか (地上波 8 本で 20 MB/s)
- 覚えた PAT・PMT で流し始める仕組み (`PsiMemory`) を最初の実装に入れるか
- `/denpa/decode` の応答の項目 (付属のエージェントの `Scramble.Decode` の JSON をそのまま写すか)
- macOS で DriverHost_PX4 を root で動かしたときの socket の権限 (全ユーザーが繋げる) と、エージェントを一般ユーザーで動かす構成
- `cmd` の recisdb の書き方 (`ISDBC-C30_2`・`J83B-H044`) は recisdb の未リリースの枝 (kazuki0824/recisdb-rs#202) のもの。リリースされたら引数の形を確かめ直す
- nasne の置き方 B で、実際の受信と録画が通るか。公式カーネル (2.6.29) で Go のバイナリが動き、エージェントが起動して API が通るところまでは実機で確かめた ([6.5](#65-sony-nasne-sonynasne-debian-の-nasne-recpt1))。そのときはアンテナを繋いでいない (B-CAS も無い) nasne だった。**その後 (2026-11-20、B-CAS を挿して) 地デジの ch=21 (フジテレビ、sid 1056) で、受信した放送の TS が流れること、`--sid` で局を絞れること (`nasne-recpt1 21 - - --sid 1056`)、約 16.5 Mbps を TLS で 30 秒流せること (PC のクライアントで 4 回、同期ずれ 0) は確かめた** (6.5 の負荷)。未確認なのは、denpa 本体から録画・視聴を最後まで通すこと、数十分以上の長時間、衛星 (BS・CS) と `--dual-tuner`。6.5 の負荷の数字とメモリの試験は、その長時間・録画の確認とは別のもの
- `nasne-recpt1` の HTTP に `--tsid` に当たる指定があるか (無ければ置き方 A では衛星のスロット番号だけ)
- `singleService` のために denpa の録画の選局に `service` を付ける直しを、denpa 本体のどのバージョンで入れるか。付かない denpa と組んだときは、録画の選局を 400 で断って記録に出す
- nasne の `--dual-tuner` の既知の問題 (待機プロセスが増える・2 本目の映像の開始が遅れる) が、denpa の切り替えの頻度で起きるか
