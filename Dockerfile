FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go test -tags sqlite_fts5 ./...
RUN CGO_ENABLED=1 go build -tags sqlite_fts5 -trimpath -o /agenticarchive ./cmd/agenticarchive

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates poppler-utils tesseract-ocr tesseract-ocr-deu tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 archive && useradd --uid 10001 --gid archive --no-create-home archive \
    && mkdir -p /data /archive && chown archive:archive /data
COPY --from=build /agenticarchive /usr/local/bin/agenticarchive
ENV ARCHIVE_ROOT=/archive DATA_DIR=/data LISTEN_ADDR=0.0.0.0:8080 \
    GOMEMLIMIT=160MiB OMP_THREAD_LIMIT=1
USER archive
EXPOSE 8080
ENTRYPOINT ["agenticarchive"]
CMD ["serve"]
