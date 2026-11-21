// このディレクトリは、Go のパッケージではなく、専用の GOROOT に当てるファイルの置き場です (scripts/build-nasne.sh)。
// go.mod を置いて、ルートのモジュールの go build ./...・go vet ./... に拾われないようにしている
// (GOARCH=mipsle だと、取り込んだ .go が、標準ライブラリの外で、コンパイルできずに失敗する)。
module github.com/denpa-px4-agent/denpa-px4-agent/third_party/x-crypto-mipsle

go 1.23
