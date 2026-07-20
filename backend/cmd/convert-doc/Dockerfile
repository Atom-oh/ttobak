# Builds the convert-doc Lambda as a container image (LibreOffice has no
# equivalent of the ARM64 zip-Lambda build used by the other 5 Go functions --
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
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      libreoffice-impress \
      fonts-noto-cjk \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/convert-doc /main
ENTRYPOINT ["/main"]
