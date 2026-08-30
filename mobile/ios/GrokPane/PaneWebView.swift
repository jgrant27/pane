import SwiftUI
import UIKit
import WebKit

struct PaneWebView: UIViewRepresentable {
    let url: URL
    var resumeToken: Int = 0

    func makeCoordinator() -> Coordinator { Coordinator() }

    func makeUIView(context: Context) -> WKWebView {
        let config = WKWebViewConfiguration()
        config.allowsInlineMediaPlayback = true
        config.defaultWebpagePreferences.allowsContentJavaScript = true
        let view = WKWebView(frame: .zero, configuration: config)
        view.navigationDelegate = context.coordinator
        view.uiDelegate = context.coordinator
        view.allowsBackForwardNavigationGestures = false
        view.scrollView.keyboardDismissMode = .interactive
        view.scrollView.contentInsetAdjustmentBehavior = .never
        view.isOpaque = false
        view.backgroundColor = .clear
        view.load(URLRequest(url: url))
        return view
    }

    func updateUIView(_ view: WKWebView, context: Context) {
        if view.url?.host != url.host || view.url?.scheme != url.scheme || view.url?.port != url.port {
            view.load(URLRequest(url: url))
            return
        }
        // #58: WKWebView often keeps a WebSocket in OPEN after freeze.
        // pageshow is what term.js uses to redial and replay the live tail.
        if context.coordinator.lastResume != resumeToken {
            context.coordinator.lastResume = resumeToken
            view.evaluateJavaScript("window.dispatchEvent(new Event('pageshow'))", completionHandler: nil)
        }
    }

    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
        var lastResume = 0
        /// WebKit blocks the page until a JS panel answers and traps if it answers
        /// twice, so every way out of a panel goes through one of these.
        private final class Once {
            private var used = false

            func run(_ body: () -> Void) {
                if used { return }
                used = true
                body()
            }
        }

        func webView(_ webView: WKWebView, decidePolicyFor action: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
            guard let dest = action.request.url else {
                decisionHandler(.allow)
                return
            }
            if action.navigationType == .linkActivated, dest.scheme == "mailto" || dest.host != webView.url?.host {
                UIApplication.shared.open(dest)
                decisionHandler(.cancel)
                return
            }
            decisionHandler(.allow)
        }

        // the page cancels anchor taps and calls window.open instead, so links never
        // arrive as .linkActivated navigations. WKWebView answers window.open with nil
        // unless this is implemented, which is why taps on markdown links do nothing.
        func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration, for action: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
            if let dest = action.request.url, let scheme = dest.scheme?.lowercased(),
               scheme == "http" || scheme == "https" || scheme == "mailto" {
                UIApplication.shared.open(dest)
            }
            return nil
        }

        // without a UI delegate WKWebView answers window.prompt with nil immediately,
        // so the project-folder and pane-URL menu actions silently do nothing.
        func webView(_ webView: WKWebView, runJavaScriptTextInputPanelWithPrompt prompt: String, defaultText: String?, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping (String?) -> Void) {
            let answered = Once()
            guard let host = presenter(for: webView) else {
                answered.run { completionHandler(nil) }
                return
            }
            let sheet = UIAlertController(title: nil, message: prompt, preferredStyle: .alert)
            sheet.addTextField { field in
                field.text = defaultText
                field.autocapitalizationType = .none
                field.autocorrectionType = .no
            }
            sheet.addAction(UIAlertAction(title: "Cancel", style: .cancel) { _ in
                answered.run { completionHandler(nil) }
            })
            sheet.addAction(UIAlertAction(title: "OK", style: .default) { [weak sheet] _ in
                answered.run { completionHandler(sheet?.textFields?.first?.text ?? "") }
            })
            present(sheet, on: host) { answered.run { completionHandler(nil) } }
        }

        func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
            let answered = Once()
            guard let host = presenter(for: webView) else {
                answered.run { completionHandler() }
                return
            }
            let sheet = UIAlertController(title: nil, message: message, preferredStyle: .alert)
            sheet.addAction(UIAlertAction(title: "OK", style: .default) { _ in
                answered.run { completionHandler() }
            })
            present(sheet, on: host) { answered.run { completionHandler() } }
        }

        /// UIKit refuses to present onto a controller that is already presenting or
        /// is not in a window, and it refuses silently — no action of the sheet can
        /// ever run, so a refusal has to answer WebKit itself or the page is frozen
        /// for good.
        private func present(_ sheet: UIAlertController, on host: UIViewController, refused: () -> Void) {
            host.present(sheet, animated: true)
            if host.presentedViewController !== sheet {
                refused()
            }
        }

        // an alert put on a root controller that something else already covers never
        // reaches the screen, so aim at whatever is frontmost instead.
        private func presenter(for webView: WKWebView) -> UIViewController? {
            var vc = webView.window?.rootViewController
            while let next = vc?.presentedViewController {
                vc = next
            }
            return vc
        }
    }
}
