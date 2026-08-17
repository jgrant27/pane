PREFIX ?= $(HOME)/.local
BIN    := pane
APP    := grok-pane
UNAME  := $(shell uname)
ifeq ($(UNAME),Darwin)
CGO_LDFLAGS += -framework UniformTypeIdentifiers
endif

.PHONY: all build install run desktop desktop-app icon test clean

all: build

build:
	go build -o $(BIN) .

install: build
	install -d $(PREFIX)/bin
	install -m 755 $(BIN) $(PREFIX)/bin/$(BIN)

run: build
	./$(BIN) $(ARGS)

icon:
	go run ./cmd/mkicon desktop/build/appicon.png
	sips -z 256 256 desktop/build/appicon.png --out web/favicon.png >/dev/null 2>&1 || cp desktop/build/appicon.png web/favicon.png

test:
	go test $(shell go list ./... | grep -v '/desktop$$')

desktop: icon build
	CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" go build -tags production -o $(APP) ./desktop

desktop-app: desktop
	rm -rf "Grok Pane.app"
	mkdir -p "Grok Pane.app/Contents/MacOS" "Grok Pane.app/Contents/Resources"
	cp $(APP) "Grok Pane.app/Contents/MacOS/grok-pane"
	cp $(BIN) "Grok Pane.app/Contents/MacOS/pane"
	cp desktop/Info.plist "Grok Pane.app/Contents/Info.plist"
	@if command -v sips >/dev/null && command -v iconutil >/dev/null; then \
		rm -rf desktop/build/appicon.iconset; \
		mkdir -p desktop/build/appicon.iconset; \
		sips -z 16 16     desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_16x16.png >/dev/null; \
		sips -z 32 32     desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_16x16@2x.png >/dev/null; \
		sips -z 32 32     desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_32x32.png >/dev/null; \
		sips -z 64 64     desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_32x32@2x.png >/dev/null; \
		sips -z 128 128   desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_128x128.png >/dev/null; \
		sips -z 256 256   desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_128x128@2x.png >/dev/null; \
		sips -z 256 256   desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_256x256.png >/dev/null; \
		sips -z 512 512   desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_256x256@2x.png >/dev/null; \
		sips -z 512 512   desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_512x512.png >/dev/null; \
		sips -z 1024 1024 desktop/build/appicon.png --out desktop/build/appicon.iconset/icon_512x512@2x.png >/dev/null; \
		iconutil -c icns desktop/build/appicon.iconset -o "Grok Pane.app/Contents/Resources/appicon.icns"; \
	else \
		cp desktop/build/appicon.png "Grok Pane.app/Contents/Resources/appicon.png"; \
	fi
	codesign --force --deep -s - "Grok Pane.app" 2>/dev/null || true
	@echo "built Grok Pane.app"

clean:
	rm -f $(BIN) $(APP)
	rm -rf "Grok Pane.app" desktop/build/appicon.iconset
