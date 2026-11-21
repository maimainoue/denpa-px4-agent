# denpa-px4-agent の Docker イメージです。仕様書 16 章。
# ビルド: docker buildx build --platform linux/amd64,linux/arm64 \
#           --build-arg VERSION=v0.1.0 -t ghcr.io/maimainoue/denpa-px4-agent:latest .
# 実行は denpa の compose.override.yml (deploy/compose.override.yml) を見てください。
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/denpa-px4-agent ./cmd/denpa-px4-agent

# 実行イメージは distroless/static です。USER は root のままにします
# (デバイスファイルのグループが video で、コンテナの中のユーザーに合わせられないため。
#  要れば --group-add video で変えます)。
FROM gcr.io/distroless/static
COPY --from=build /out/denpa-px4-agent /denpa-px4-agent
EXPOSE 25252
ENTRYPOINT ["/denpa-px4-agent"]
