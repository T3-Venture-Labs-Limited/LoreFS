VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.1.0-dev)
COMMIT_TIME := $(shell git log -1 --format=%ct 2>/dev/null || echo 0)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commitTime=$(COMMIT_TIME)

.PHONY: build test itest slowtest dist check clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/lorefs ./cmd/lorefs

test:
	go vet ./...
	go test ./...

itest: build
	scripts/itest.sh

# The same local drive seen from a computer whose requests to the index
# take half a second longer (a VPN, another continent). About 3 minutes.
slowtest: build
	scripts/slowlink-test.sh

check: test
	scripts/check-private.sh
	scripts/check-private-test.sh

dist: build
	scripts/make-dist.sh $(VERSION)

clean:
	rm -rf bin dist/*.tar.gz dist/*.sha256
