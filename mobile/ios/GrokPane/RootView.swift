import SwiftUI

struct RootView: View {
    @AppStorage("pane-url") private var urlString = ""
    @State private var draft = ""
    @State private var askURL = false

    var body: some View {
        ZStack(alignment: .topTrailing) {
            if let url = PaneURL.parse(urlString) {
                PaneWebView(url: url)
                    .ignoresSafeArea()
            } else {
                VStack(spacing: 16) {
                    Text("Grok Pane")
                        .font(.title2.weight(.semibold))
                    Text("The agent stays on your computer.\nThis app is the window.")
                        .multilineTextAlignment(.center)
                        .foregroundStyle(.secondary)
                    Button("Connect to pane…") { beginEdit() }
                        .buttonStyle(.borderedProminent)
                }
                .padding()
            }
            Button(action: beginEdit) {
                Image(systemName: "link.circle.fill")
                    .font(.title2)
                    .symbolRenderingMode(.hierarchical)
                    .padding(12)
            }
            .accessibilityLabel("Pane URL")
        }
        .alert("Pane URL", isPresented: $askURL) {
            TextField("https://host.ts.net", text: $draft)
                .textInputAutocapitalization(.never)
                .keyboardType(.URL)
            Button("Save") { urlString = draft.trimmingCharacters(in: .whitespacesAndNewlines) }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Tailscale or LAN address of pane. Example: https://beelz.tailnet.ts.net")
        }
    }

    private func beginEdit() {
        draft = urlString
        askURL = true
    }
}

enum PaneURL {
    static func parse(_ raw: String) -> URL? {
        var s = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if s.isEmpty { return nil }
        if !s.contains("://") { s = "https://" + s }
        guard let u = URL(string: s), let scheme = u.scheme, let host = u.host, !host.isEmpty else { return nil }
        if scheme != "http" && scheme != "https" { return nil }
        return u
    }
}
