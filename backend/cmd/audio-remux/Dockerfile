# audio-remux needs ffmpeg/ffprobe, which the zip-deployed Go Lambdas lack.
FROM golang:1.27.3-bookworm@sha256:dc0963f32053e4bc451d24d87e3892d42d01021b61f093e98eb95c0352bb84b0 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -tags lambda.norpc -o /out/audio-remux ./cmd/audio-remux

# --platform keeps the runtime layer arm64 on amd64 builders (see convert-doc).
# ca-certificates is required for the AWS SDK's TLS to S3.
FROM --platform=linux/arm64 debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates \
      ffmpeg \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/audio-remux /main
ENTRYPOINT ["/main"]
