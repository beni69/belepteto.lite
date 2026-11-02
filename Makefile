BINARY := belepteto

.PHONY: build build-static run test tidy clean

build:
	go build -o bin/$(BINARY) .

# build-static produces a fully static, self-contained binary. CGO_ENABLED=0
# forces pure-Go (the SQLite driver is already cgo-free), so the result has no
# libc dependency and runs on any matching GOOS/GOARCH. Override GOOS/GOARCH to
# cross-compile, e.g. `make build-static GOOS=linux GOARCH=arm64`.
build-static:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin/$(BINARY) .

run:
	BELEPTETO_LOG_LEVEL=debug BELEPTETO_ADMIN_EMAIL=admin@example.com BELEPTETO_ADMIN_PASSWORD=xd BELEPTETO_MQTT_ADDR="0.0.0.0:1883" BELEPTETO_CONTROLLER_ID=I37C go run .

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -rf bin
