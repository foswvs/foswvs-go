.PHONY: build build-arm run clean install dev dev-up dev-down dev-logs prod-up

BINARY=foswvs-go
MAIN=./cmd/main.go

# Build for current platform
build:
	go build -o $(BINARY) $(MAIN)

# Cross-compile for Raspberry Pi (ARMv7 / armhf)
build-arm:
	GOOS=linux GOARCH=arm GOARM=7 go build -o $(BINARY)-arm $(MAIN)

# Cross-compile for Raspberry Pi (ARM64 / aarch64)
build-arm64:
	GOOS=linux GOARCH=arm64 go build -o $(BINARY)-arm64 $(MAIN)

# Run locally
run:
	go run $(MAIN) -addr :8080 -data-dir ./data -web-dir ./web/static

# Clean build artifacts
clean:
	rm -f $(BINARY) $(BINARY)-arm $(BINARY)-arm64

# Deploy to Raspberry Pi (edit PI_HOST)
PI_HOST ?= pi@raspberrypi.local
PI_DIR  ?= /home/pi/foswvs-go

deploy: build-arm
	ssh $(PI_HOST) "mkdir -p $(PI_DIR)/web"
	scp $(BINARY)-arm $(PI_HOST):$(PI_DIR)/$(BINARY)
	scp -r web/static $(PI_HOST):$(PI_DIR)/web/
	scp foswvs-go.service $(PI_HOST):/tmp/
	scp scripts/iptables-base.sh $(PI_HOST):/tmp/foswvs-iptables-base.sh
	ssh $(PI_HOST) "sudo cp /tmp/foswvs-go.service /lib/systemd/system/ && sudo cp /tmp/foswvs-iptables-base.sh /usr/local/bin/foswvs-iptables-base.sh && sudo chmod +x /usr/local/bin/foswvs-iptables-base.sh && sudo systemctl daemon-reload && sudo systemctl enable foswvs-go && sudo systemctl restart foswvs-go"
	@echo "Deployed! Check: ssh $(PI_HOST) 'sudo journalctl -u foswvs-go -f'"

# --- Docker dev ---

# Start dev with hot reload
dev:
	docker compose up dev --build

dev-up:
	docker compose up dev --build -d

dev-down:
	docker compose down -v

dev-logs:
	docker compose logs -f dev

# Production-like container
prod-up:
	docker compose --profile prod up prod --build

# Install on the Pi itself
install:
	go build -o /usr/local/bin/$(BINARY) $(MAIN)
	mkdir -p $(PI_DIR)/web/static
	cp -r web/static/* $(PI_DIR)/web/static/
	cp scripts/iptables-base.sh /usr/local/bin/foswvs-iptables-base.sh
	chmod +x /usr/local/bin/foswvs-iptables-base.sh
	cp foswvs-go.service /lib/systemd/system/
	systemctl daemon-reload
	systemctl enable foswvs-go
	@echo "Installed. Run: sudo systemctl start foswvs-go"
