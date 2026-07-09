FROM golang:1.22-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download || true
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /foswvs-go ./cmd/main.go

# --- Runtime ---
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    iptables \
    iproute2 \
    procps \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Create app user and data directory
RUN useradd -m -s /bin/bash pisodev \
    && mkdir -p /home/pisodev/foswvs-go/web/static \
    && chown -R pisodev:pisodev /home/pisodev/foswvs-go

COPY --from=builder /foswvs-go /usr/local/bin/foswvs-go
COPY web/static/ /home/pisodev/foswvs-go/web/static/
COPY conf/ /home/pisodev/foswvs-go/conf/

# Default rates config
RUN echo '{"1":24,"5":128,"10":1024}' > /home/pisodev/foswvs-go/rates.json \
    && chown -R pisodev:pisodev /home/pisodev/foswvs-go

EXPOSE 8080

ENV FOSWVS_DEV=1

ENTRYPOINT ["foswvs-go"]
CMD ["-addr", ":8080", "-data-dir", "/home/pisodev/foswvs-go", "-web-dir", "/home/pisodev/foswvs-go/web/static"]
