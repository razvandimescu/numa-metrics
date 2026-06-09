BINARY := numa-metrics
LDFLAGS := -s -w

.PHONY: build pi consumer test vet clean

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

# Pi Zero v1 is ARMv6. CGO off -> fully static, no musl/Docker needed.
pi:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 \
		go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-armv6 .

# Laptop-side drain consumer (pure-Go SQLite). Usually run via deploy/ compose.
consumer:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o drain-consumer ./cmd/drain-consumer

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY) $(BINARY)-armv6 drain-consumer *.db *.db-wal *.db-shm
