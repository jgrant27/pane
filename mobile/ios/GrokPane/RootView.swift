import SwiftUI

struct RootView: View {
    #if targetEnvironment(simulator)
    @AppStorage("pane-url") private var urlString = "http://127.0.0.1:7420"
    #else
    @AppStorage("pane-url") private var urlString = ""
    #endif
    @State private var draft = ""
    @State private var askURL = false

    var body: some View {
        ZStack(alignment: .topTrailing) {
            if let url = paneURL {
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
            Text("Tailscale or LAN address of pane. Examples: beelz.tailnet.ts.net or 192.168.1.5:7420")
        }
        .onAppear { seedURL() }
    }

    /// Simulator shares the Mac loopback, so an empty URL still loads pane.
    /// A device has no pane on itself — empty stays the connect prompt.
    /// make ios sets PANE_URL via SIMCTL_CHILD_PANE_URL; that wins when present.
    private var paneURL: URL? {
        if let env = ProcessInfo.processInfo.environment["PANE_URL"], let u = PaneURL.parse(env) {
            return u
        }
        if let u = PaneURL.parse(urlString) { return u }
        #if targetEnvironment(simulator)
        return URL(string: "http://127.0.0.1:7420")
        #else
        return nil
        #endif
    }

    private func seedURL() {
        if let env = ProcessInfo.processInfo.environment["PANE_URL"] {
            let t = env.trimmingCharacters(in: .whitespacesAndNewlines)
            if PaneURL.parse(t) != nil {
                urlString = t
                return
            }
        }
        if PaneURL.parse(urlString) != nil { return }
        #if targetEnvironment(simulator)
        urlString = "http://127.0.0.1:7420"
        #endif
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
        if !s.contains("://") { s = (plainHost(authorityHost(s)) ? "http://" : "https://") + s }
        guard let u = URL(string: s), let scheme = u.scheme?.lowercased(), let host = u.host, !host.isEmpty else { return nil }
        if scheme != "http" && scheme != "https" { return nil }
        return u
    }

    /// Host of an authority-first string ("host:port/path"), lowercased, brackets stripped.
    static func authorityHost(_ raw: String) -> String {
        var h = raw
        if let cut = h.firstIndex(where: { $0 == "/" || $0 == "?" || $0 == "#" }) {
            h = String(h[h.startIndex..<cut])
        }
        if let at = h.lastIndex(of: "@") {
            h = String(h[h.index(after: at)...])
        }
        if h.hasPrefix("["), let end = h.firstIndex(of: "]") {
            return String(h[h.index(after: h.startIndex)..<end]).lowercased()
        }
        if let colon = h.firstIndex(of: ":") {
            h = String(h[h.startIndex..<colon])
        }
        return h.lowercased()
    }

    /// True for hosts pane can serve without TLS. pane itself speaks cleartext;
    /// https only exists where a tailscale-serve front end terminates it, i.e. on
    /// the MagicDNS name — never on a loopback or LAN address, so defaulting a
    /// schemeless LAN entry to https makes it fail before it starts.
    static func plainHost(_ host: String) -> Bool {
        if host.isEmpty { return false }
        if host == "localhost" || host.hasSuffix(".localhost") || host.hasSuffix(".local") { return true }
        if host.contains(":") {
            return host == "::1" || host.hasPrefix("fe80:") || host.hasPrefix("fc") || host.hasPrefix("fd")
        }
        let fields = host.split(separator: ".", omittingEmptySubsequences: false)
        let octets = fields.compactMap { Int($0) }
        if fields.count == 4, octets.count == 4, octets.allSatisfy({ $0 >= 0 && $0 <= 255 }) {
            return octets[0] == 127 || octets[0] == 10
                || (octets[0] == 172 && (16...31).contains(octets[1]))
                || (octets[0] == 192 && octets[1] == 168)
                || (octets[0] == 169 && octets[1] == 254)
                || (octets[0] == 100 && (64...127).contains(octets[1]))
        }
        // an unqualified name is a LAN or tailnet short name, not a public host.
        return !host.contains(".")
    }
}
