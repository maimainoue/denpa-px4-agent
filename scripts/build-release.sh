#!/usr/bin/env bash
# リリース用のバイナリを作ります。dist/ に出します。
# 使い方: VERSION=v0.1.0 bash scripts/build-release.sh
#   VERSION を書かなければ dev になります。go が PATH に要ります。
set -euo pipefail
cd "$(dirname "$0")/.."

command -v go >/dev/null || { echo "go がありません (PATH を見てください)" >&2; exit 1; }

# GNU tar・zip・sha256sum・file を使います (実質 Linux 専用)。
for c in zip tar sha256sum file; do
	command -v "$c" >/dev/null || { echo "$c がありません (PATH を見てください)" >&2; exit 1; }
done

VERSION="${VERSION:-dev}"
DIST=dist
rm -rf "$DIST"
mkdir -p "$DIST"

LDFLAGS="-s -w -X main.version=${VERSION}"

# build GOOS GOARCH 出力名 タグ [追加の環境変数...]
build() {
	local goos=$1 goarch=$2 out=$3 tags=$4
	shift 4
	echo "== $goos/$goarch -> $out"
	env CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" "$@" \
		go build -trimpath -tags "$tags" -ldflags "$LDFLAGS" -o "$DIST/$out" ./cmd/denpa-px4-agent
}

build linux amd64 denpa-px4-agent-linux-amd64 ""
build linux arm64 denpa-px4-agent-linux-arm64 ""
build windows amd64 denpa-px4-agent.exe ""
build darwin arm64 denpa-px4-agent-darwin-arm64 ""
build darwin amd64 denpa-px4-agent-darwin-amd64 ""
# nasne 向け (linux/mipsle) は、ChaCha20-Poly1305 のアセンブリ入りの専用ビルドです (scripts/build-nasne.sh、
# 仕様書 16 章)。取り込んだアセンブリ (third_party/x-crypto-mipsle) を、専用に複製した GOROOT に当てるので、
# ほかのターゲットより時間がかかります。TLS の暗号が約 4.5 倍速く、nasne の CPU が約半分になります。
# **MIPS32r2 以上の機材だけで動きます** (nasne の 74Kc は r2)。試験的です (cmd バックエンドだけで、
# pcscd と chardev は使いません。nasne・nethttpomithttp2 のタグは、build-nasne.sh が付けます)。
# バイナリのとなりに、同梱するライセンス (アセンブリの MIT と、Go の BSD) の *.licenses.txt もできます。
# 配るときは一緒に付けます (下の sha256sums.txt にも入ります)。
echo "== linux/mipsle (アセンブリ入り) -> denpa-px4-agent-linux-mipsle"
VERSION="$VERSION" bash scripts/build-nasne.sh "$DIST/denpa-px4-agent-linux-mipsle"

# Windows の zip: 中身は denpa-px4-agent.exe と README です (仕様書 16 章)。
zip -j -q "$DIST/denpa-px4-agent-windows-amd64.zip" "$DIST/denpa-px4-agent.exe" README.md

# macOS の tar.gz: 中身は denpa-px4-agent と README です (仕様書 16 章)。
for arch in arm64 amd64; do
	stage="$DIST/stage-darwin-$arch"
	mkdir -p "$stage"
	cp "$DIST/denpa-px4-agent-darwin-$arch" "$stage/denpa-px4-agent"
	cp README.md "$stage/README.md"
	tar --sort=name --owner=0 --group=0 -czf \
		"$DIST/denpa-px4-agent-darwin-$arch.tar.gz" -C "$stage" denpa-px4-agent README.md
	rm -rf "$stage"
done

# チェックサム (sha256sums.txt 自身は入れません)。
(cd "$DIST" && sha256sum denpa-px4-agent-* > sha256sums.txt)

echo
echo "== できました (VERSION=$VERSION)"
ls -la "$DIST"
echo
echo "== linux/amd64 の --version の確認"
if [ "$(uname -s)" = Linux ] && [ "$(uname -m)" = x86_64 ]; then
	"$DIST/denpa-px4-agent-linux-amd64" --version
else
	echo "(この機械では linux/amd64 を実行できないので、--version の確認はしません)"
fi
file "$DIST"/denpa-px4-agent-*
