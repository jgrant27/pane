import SwiftUI
import UIKit
import WebKit

struct PaneWebView: UIViewRepresentable {
    let url: URL

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
        }
    }

    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
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
            guard let host = presenter(for: webView) else {
                completionHandler(nil)
                return
            }
            let sheet = UIAlertController(title: nil, message: prompt, preferredStyle: .alert)
            sheet.addTextField { field in
                field.text = defaultText
                field.autocapitalizationType = .none
                field.autocorrectionType = .no
            }
            sheet.addAction(UIAlertAction(title: "Cancel", style: .cancel) { _ in
                completionHandler(nil)
            })
            sheet.addAction(UIAlertAction(title: "OK", style: .default) { [weak sheet] _ in
                completionHandler(sheet?.textFields?.first?.text ?? "")
            })
            host.present(sheet, animated: true)
        }

        func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String, initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping () -> Void) {
            guard let host = presenter(for: webView) else {
                completionHandler()
                return
            }
            let sheet = UIAlertController(title: nil, message: message, preferredStyle: .alert)
            sheet.addAction(UIAlertAction(title: "OK", style: .default) { _ in completionHandler() })
            host.present(sheet, animated: true)
        }

        // WebKit requires the completion handler to run exactly once, so present on
        // whatever is frontmost rather than a root controller that may be covered.
        private func presenter(for webView: WKWebView) -> UIViewController? {
            var vc = webView.window?.rootViewController
            while let next = vc?.presentedViewController {
                vc = next
            }
            return vc
        }
    }
}
