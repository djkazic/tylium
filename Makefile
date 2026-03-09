.PHONY: build clean test

BINARIES = tyld tylminer tylcli tylpush tylbuild tylsim

build:
	@for bin in $(BINARIES); do \
		echo "building $$bin..."; \
		go build -o $$bin ./cmd/$$bin; \
	done
	@echo "done"

clean:
	rm -f $(BINARIES)

test:
	go test ./...
