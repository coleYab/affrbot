.PHONY: all build run tidy clean

all: build

build:
	go build -o afrobot .

run:
	go run .

tidy:
	go mod tidy

clean:
	rm -f afrobot