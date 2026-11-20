#!/bin/sh
# Go の GOROOT を複製して、golang/crypto PR 294 (MIPSLE の ChaCha20・Poly1305 のアセンブリ) を当てる。
# 元の GOROOT は触らない。使い方は README.md。
#
#   apply.sh <複製先> <x/crypto の PR 294 の作業ツリー> [元の GOROOT]
set -eu
DST=${1:?複製先}
PR=${2:?x/crypto の PR 294 の作業ツリー}
SRC=${3:-$(go env GOROOT)}

if [ -e "$DST" ] || [ -L "$DST" ]; then
  echo "複製先がもうある: $DST" >&2
  exit 1
fi

case "$SRC" in
  /*) ;;
  *) SRC=$(pwd -P)/$SRC ;;
esac
SRC=$(CDPATH= cd -P "$SRC" && pwd -P)

canonicalize_destination() (
  path=$1
  case "$path" in
    /*) ;;
    *) path=$(pwd -P)/$path ;;
  esac

  current=/
  remainder=
  set -f
  old_ifs=$IFS
  IFS=/
  set -- $path
  IFS=$old_ifs
  for component do
    case "$component" in
      ""|.) continue ;;
      ..)
        if [ -n "$remainder" ]; then
          remainder=${remainder%/*}
        else
          current=$(CDPATH= cd -P "$current/.." && pwd -P) || exit 1
        fi
        ;;
      *)
        if [ -z "$remainder" ] && [ -d "$current/$component" ]; then
          current=$(CDPATH= cd -P "$current/$component" && pwd -P) || exit 1
        else
          remainder=$remainder/$component
        fi
        ;;
    esac
  done
  printf '%s\n' "$current$remainder"
)

DST_REAL=$(canonicalize_destination "$DST")
case "$DST_REAL" in
  "$SRC"|"$SRC"/*)
    echo "複製先は GOROOT の外に指定してください: $DST" >&2
    exit 1
    ;;
esac

for f in chacha20/chacha_mipsle.go chacha20/chacha_mipsle.s internal/poly1305/sum_mipsle.go internal/poly1305/sum_mipsle.s; do
  [ -f "$PR/$f" ] || { echo "PR 294 の作業ツリーに $f が無い" >&2; exit 1; }
done

echo "GOROOT を複製: $SRC -> $DST"
mkdir -p "$DST"
cp -R "$SRC/." "$DST/"
chmod -R u+w "$DST"
V="$DST/src/vendor/golang.org/x/crypto"

cp "$PR/chacha20/chacha_mipsle.go" "$PR/chacha20/chacha_mipsle.s" "$V/chacha20/"
cp "$PR/internal/poly1305/sum_mipsle.go" "$PR/internal/poly1305/sum_mipsle.s" "$V/internal/poly1305/"

# 汎用実装のビルドタグから mipsle を外す (mipsle ではアセンブリを使う)
patch_tag() { # <ファイル> <元の式> <新しい式> (行ごと置き換える。sed の s は & の扱いが面倒なので使わない)
  L=$(grep -n -F -x "//go:build $2" "$1" | cut -d: -f1 | head -1)
  [ -n "$L" ] || { echo "ビルドタグが想定と違う: $1 (Go のバージョンが違う?)" >&2; exit 1; }
  sed -i "${L}c\
//go:build $3" "$1"
}
patch_tag "$V/chacha20/chacha_noasm.go" \
  '(!arm64 && !s390x && !ppc64 && !ppc64le) || !gc || purego' \
  '(!arm64 && !s390x && !ppc64 && !ppc64le && !mipsle) || !gc || purego'
patch_tag "$V/internal/poly1305/mac_noasm.go" \
  '(!amd64 && !loong64 && !ppc64le && !ppc64 && !s390x) || !gc || purego' \
  '(!amd64 && !loong64 && !ppc64le && !ppc64 && !s390x && !mipsle) || !gc || purego'

echo "できた: $DST"
echo "ビルドの例:"
echo "  GOROOT=$DST PATH=$DST/bin:\$PATH GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \\"
echo "    go build -asmflags='all=-D=GOMIPS_r2' ./cmd/denpa-px4-agent"
