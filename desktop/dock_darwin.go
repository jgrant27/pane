//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>
void SetDockIconPNG(const void *data, int len) {
	@autoreleasepool {
		if (data == NULL || len <= 0) {
			return;
		}
		NSData *d = [NSData dataWithBytes:data length:(NSUInteger)len];
		NSImage *img = [[NSImage alloc] initWithData:d];
		if (img != nil) {
			[NSApp setApplicationIconImage:img];
		}
	}
}
*/
import "C"
import "unsafe"

func setDockIcon(png []byte) {
	if len(png) == 0 {
		return
	}
	C.SetDockIconPNG(unsafe.Pointer(&png[0]), C.int(len(png)))
}
