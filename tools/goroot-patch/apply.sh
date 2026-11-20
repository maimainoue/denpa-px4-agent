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

SRC=$(CDPATH= cd -P "$SRC" && pwd -P)

canonicalize_destination() (
  path=$1
  case "$path" in
    [A-Za-z]:[\\/]*)
      # MSYS・Cygwin では Windows のパス (mkdir・cp もそう読む)。POSIX の形にして検査する。
      # cygpath が無い環境では、ただの相対パス (名前が "C:" のディレクトリ)
      if command -v cygpath >/dev/null 2>&1; then
        path=$(cygpath -u "$path") || exit 1
      fi
      ;;
  esac
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
          current=$(CDPATH= cd -P "${current%/}/.." && pwd -P) || exit 1
        fi
        ;;
      *)
        if [ -z "$remainder" ] && [ -d "${current%/}/$component" ]; then
          current=$(CDPATH= cd -P "${current%/}/$component" && pwd -P) || exit 1
        else
          remainder=$remainder/$component
        fi
        ;;
    esac
  done
  printf '%s\n' "${current%/}$remainder"
)

DST_REAL=$(canonicalize_destination "$DST")
# ".." を解いた結果が、既にあるパスになることがある (<無いディレクトリ>/../sdk → sdk)
if [ -e "$DST_REAL" ] || [ -L "$DST_REAL" ]; then
  echo "複製先がもうある: $DST_REAL" >&2
  exit 1
fi
case "$DST_REAL" in
  "$SRC"|"$SRC"/*)
    echo "複製先は GOROOT の外に指定してください: $DST" >&2
    exit 1
    ;;
esac
# 作成・コピー・権限の変更は、検査したパスと同じものに対して行う (元の文字列には ".." や別の書き方が残りうる)
DST=$DST_REAL

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
