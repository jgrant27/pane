PREFIX ?= $(HOME)/.local
BIN    := pane

.PHONY: all build install run clean

all: build

build:
	go build -o $(BIN) .

install: build
	install -d $(PREFIX)/bin
	install -m 755 $(BIN) $(PREFIX)/bin/$(BIN)

run: build
	./$(BIN) $(ARGS)

clean:
	rm -f $(BIN)
