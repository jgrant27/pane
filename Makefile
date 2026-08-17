PREFIX ?= $(HOME)/.local
BIN    := pane
APP    := grok-pane
UNAME  := $(shell uname)
ifeq ($(UNAME),Darwin)
CGO_LDFLAGS += -framework UniformTypeIdentifiers
endif
DESKTOP_TAGS := production
ifeq ($(UNAME),Linux)
  ifeq ($(shell pkg-config --exists webkit2gtk-4.1 && echo yes),yes)
    DESKTOP_TAGS := production,webkit2_41
  endif
endif

.PHONY: all build install run desktop desktop-app icon test clean desktop-linux desktop-linux-amd64 desktop-linux-arm64 qemu-binfmt

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
	CGO_ENABLED=1 CGO_LDFLAGS="$(CGO_LDFLAGS)" go build -tags "$(DESKTOP_TAGS)" -o $(APP) ./desktop

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

# Linux binaries via Docker Buildx + QEMU (amd64 and arm64).
# Installs QEMU binfmt and a buildx builder; needs Docker with --privileged.
desktop-linux: qemu-binfmt desktop-linux-amd64 desktop-linux-arm64

qemu-binfmt:
	@command -v docker >/dev/null || { echo "pane: docker is required for desktop-linux"; exit 1; }
	@docker info >/dev/null 2>&1 || { echo "pane: start Docker first"; exit 1; }
	@docker buildx version >/dev/null 2>&1 || { echo "pane: docker buildx is required"; exit 1; }
	@docker buildx inspect pane >/dev/null 2>&1 || docker buildx create --name pane --driver docker-container --use >/dev/null
	@docker buildx use pane >/dev/null
	@docker buildx inspect --bootstrap >/dev/null
	@docker run --privileged --rm tonistiigi/binfmt --install amd64,arm64 >/dev/null
	@echo "pane: qemu/binfmt ready"

desktop-linux-amd64: qemu-binfmt
	docker buildx build --builder pane --platform linux/amd64 -f ci/Dockerfile.linux \
		--output type=local,dest=dist/linux-amd64 .

desktop-linux-arm64: qemu-binfmt
	docker buildx build --builder pane --platform linux/arm64 -f ci/Dockerfile.linux \
		--output type=local,dest=dist/linux-arm64 .

clean:
	rm -f $(BIN) $(APP)
	rm -rf "Grok Pane.app" desktop/build/appicon.iconset dist
