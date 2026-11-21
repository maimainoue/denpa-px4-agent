#!/usr/bin/env bash
# nasne (linux/mipsle、MIPS32r2) 向けのバイナリを作ります。
#
# ChaCha20-Poly1305 のアセンブリ (third_party/x-crypto-mipsle) を、専用に複製した GOROOT に当ててビルドし、
# ROTR を使います (-asmflags=all=-D=GOMIPS_r2)。標準の Go のビルドより、TLS の暗号が約 4.5 倍速く、
# 実放送の TLS で、エージェントの CPU が約半分になります (イシュー #3、third_party/x-crypto-mipsle/README.md)。
#
# **MIPS32r2 以上の機材だけで動きます** (nasne の 74Kc は MIPS32r2)。r1 の機材では動きません。
#
# 使い方: VERSION=v0.1.0 bash scripts/build-nasne.sh [出力ファイル]
#   出力の既定は dist/denpa-px4-agent-linux-mipsle。VERSION が無ければ dev。
#   バイナリのとなりに、同梱するライセンスを <出力>.licenses.txt に書きます (配るときは一緒に付けます)。
# 要るもの: go (third_party/x-crypto-mipsle/GO_VERSION のバージョンで確かめている)、sha256sum
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-dev}"
OUT="${1:-dist/denpa-px4-agent-linux-mipsle}"
SRCDIR=third_party/x-crypto-mipsle

if ! [[ "$VERSION" =~ ^[0-9A-Za-z._-]+$ ]]; then
	echo "VERSION は英数字と . _ - だけにしてください: $VERSION" >&2
	exit 1
fi
for c in go sha256sum; do
	command -v "$c" >/dev/null || { echo "$c がありません (PATH を見てください)" >&2; exit 1; }
done

# 取り込んだファイルが、上流のままか (SHA256SUMS)。CRLF などに変わっていると、ここで止まる。
(cd "$SRCDIR" && sha256sum -c --quiet SHA256SUMS) || {
	echo "$SRCDIR のファイルが SHA256SUMS と合いません (改行の変換などで変わっていないか)" >&2
	exit 1
}

want="$(tr -d '[:space:]' < "$SRCDIR/GO_VERSION")"
have="$(go env GOVERSION)"
if [ "$have" != "$want" ]; then
	echo "注意: go は $have です (確かめているのは $want)。ビルドタグが違えば、apply.sh が止めます" >&2
fi

WORK="$(mktemp -d)"
trap 'chmod -R u+w "$WORK" 2>/dev/null; rm -rf "$WORK"' EXIT

echo "== GOROOT を複製してアセンブリを当てる ($have)"
sh tools/goroot-patch/apply.sh "$WORK/goroot" "$SRCDIR" "$(go env GOROOT)" | sed 's/^/   /'

# Windows (Git Bash) では、GOROOT は Windows の形で渡す
if command -v cygpath >/dev/null 2>&1; then
	PGOROOT="$(cygpath -m "$WORK/goroot")"
else
	PGOROOT="$WORK/goroot"
fi

mkdir -p "$(dirname "$OUT")"
rm -f "$OUT"
build() { # build <出力> <リンカーの追加フラグ>
	env GOROOT="$PGOROOT" PATH="$WORK/goroot/bin:$PATH" \
		CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat \
		go build -trimpath -buildvcs=false -tags nasne,nethttpomithttp2 \
		-asmflags=all=-D=GOMIPS_r2 \
		-ldflags "$2 -X main.version=${VERSION}" \
		-o "$1" ./cmd/denpa-px4-agent
}

echo "== ビルド ($OUT)"
build "$OUT" "-s -w"

# アセンブリが本当に入ったか。-s を付けるとシンボルが消えるので、確認用に、-w だけで、もう 1 つ作る
echo "== 確認: アセンブリが入っているか"
build "$WORK/check" "-w"
# grep -q は、最初の一致ですぐ終わるので、まだ書いている途中の go tool nm が SIGPIPE (終了コード 141) で死に、
# pipefail でパイプライン全体が失敗になる (Linux で起きる。一致したのに「アセンブリが無い」と中止してしまう)。
# そこで grep の出力は捨てるだけにして、最後まで読ませる。シンボルが本当に無いとき (grep が 1) と、
# go tool nm 自体の失敗 (pipefail) は、これまでどおり検出できる。
for sym in 'chacha20.xorKeyStream' 'poly1305.update'; do
	if ! env GOROOT="$PGOROOT" PATH="$WORK/goroot/bin:$PATH" go tool nm "$WORK/check" | grep -E "${sym//./\\.}\$" >/dev/null; then
		echo "アセンブリが入っていません ($sym が無い)。中止します" >&2
		rm -f "$OUT"
		exit 1
	fi
done
if ! go version -m "$OUT" | grep -- '-asmflags=all=-D=GOMIPS_r2' >/dev/null; then
	echo "-D=GOMIPS_r2 が効いていません。中止します" >&2
	rm -f "$OUT"
	exit 1
fi
echo "   chacha20.xorKeyStream と poly1305.update が入っていて、-D=GOMIPS_r2 で作られている"

# 配るときに付けるライセンス (third_party/x-crypto-mipsle/README.md の「ライセンス」)
LIC="${OUT}.licenses.txt"
{
	echo "denpa-px4-agent (nasne 向け、linux/mipsle) に含まれる、第三者のコードのライセンスです。"
	echo "ChaCha20 と Poly1305 の MIPSLE のアセンブリ (golang/crypto の PR 294、コミット 2de561af3f50) です。"
	echo
	echo "================ アセンブリ (*.s): GPL-2.0 OR MIT のうち MIT を選んで使います ================"
	cat "$SRCDIR/LICENSE.MIT"
	echo
	echo "================ Go との接着 (*.go): BSD 3-Clause (The Go Authors) ================"
	cat "$SRCDIR/LICENSE.BSD"
} > "$LIC"

echo
echo "== できました (VERSION=$VERSION)"
ls -l "$OUT" "$LIC"
sha256sum "$OUT"
