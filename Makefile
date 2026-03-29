.PHONY: all build clean octi-pulpo octi-worker octi-timer

all: build

build: octi-pulpo octi-worker octi-timer

octi-pulpo:
	go build -o bin/octi-pulpo ./cmd/octi-pulpo/

octi-worker:
	go build -o bin/octi-worker ./cmd/octi-worker/

octi-timer:
	go build -o bin/octi-timer ./cmd/octi-timer/

clean:
	rm -f bin/octi-pulpo bin/octi-worker bin/octi-timer
