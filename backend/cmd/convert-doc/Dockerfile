# Builds the convert-doc Lambda as a container image (LibreOffice has no
# equivalent of the ARM64 zip-Lambda build used by the other 8 zip-deployed Go functions --
# it's a native binary + a large font/office dependency tree, not a single
# static Go binary).
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags lambda.norpc -o /out/convert-doc ./cmd/convert-doc

# fonts-noto-cjk is not optional: without it, Korean text in a converted PDF
# renders as tofu boxes -- this is a Korean product, most decks have Korean text.
# ca-certificates is not optional either: debian:bookworm-slim ships no root
# CA bundle, so the Go AWS SDK's TLS handshake to S3 fails outright
# ("x509: certificate signed by unknown authority") without it.
#
# --platform=linux/arm64 is required here (the build stage above doesn't need
# it -- GOARCH=arm64 cross-compiles regardless of the build container's own
# arch). Without it, building on an amd64 CI runner with no explicit
# `docker buildx --platform` flag pulls the amd64 debian image, so
# LibreOffice/fonts/ca-certificates install as amd64 while the COPYed Go
# binary is arm64 -- a mixed-arch image that fails at Lambda runtime, unlike
# the other 8 zip-deployed Go Lambdas which are pure ARM64 zip deploys.
FROM --platform=linux/arm64 debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      libreoffice-impress \
      fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/convert-doc /main
ENTRYPOINT ["/main"]
