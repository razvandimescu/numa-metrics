BINARY := numa-metrics
LDFLAGS := -s -w

.PHONY: build pi test vet clean

build:
	go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

# Pi Zero v1 is ARMv6. CGO off -> fully static, no musl/Docker needed.
pi:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 \
		go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY)-armv6 .

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -f $(BINARY) $(BINARY)-armv6
