BINARY := belepteto

.PHONY: build run test tidy clean

build:
	go build -o bin/$(BINARY) .

run:
	go run .

test:
	go test ./...

tidy:
	go mod tidy

clean:
	rm -rf bin
