package com.jgrant27.grokpane

import android.annotation.SuppressLint
import android.content.Context
import android.os.Bundle
import android.webkit.WebChromeClient
import android.webkit.WebResourceRequest
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.Toast
import androidx.activity.OnBackPressedCallback
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity

class MainActivity : AppCompatActivity() {
    private lateinit var web: WebView

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        web = WebView(this).apply {
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.mediaPlaybackRequiresUserGesture = false
            webChromeClient = WebChromeClient()
            webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(view: WebView, req: WebResourceRequest): Boolean {
                    val scheme = req.url.scheme?.lowercase()
                    val host = req.url.host
                    val here = view.url?.let { android.net.Uri.parse(it).host }
                    // an allowlist, not "anything with a foreign host": the URL comes
                    // straight from whatever the pane renders, and handing an arbitrary
                    // scheme to an implicit ACTION_VIEW lets page content pick which
                    // app we launch.
                    val external = scheme == "mailto" || scheme == "tel" ||
                        ((scheme == "http" || scheme == "https") &&
                            host != null && here != null && !host.equals(here, ignoreCase = true))
                    if (!external) return false
                    try {
                        startActivity(android.content.Intent(android.content.Intent.ACTION_VIEW, req.url))
                    } catch (e: android.content.ActivityNotFoundException) {
                        // a device with no mail client resolves no mailto: handler, and an
                        // uncaught ActivityNotFoundException here kills the live session.
                        Toast.makeText(
                            this@MainActivity,
                            getString(R.string.no_handler, req.url.toString()),
                            Toast.LENGTH_SHORT
                        ).show()
                    }
                    return true
                }
            }
        }
        val root = FrameLayout(this)
        root.addView(web, FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT)
        val gear = android.widget.Button(this).apply {
            text = "URL"
            contentDescription = getString(R.string.pane_url)
            setOnClickListener { askURL() }
        }
        val lp = FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.WRAP_CONTENT,
            FrameLayout.LayoutParams.WRAP_CONTENT
        )
        lp.gravity = android.view.Gravity.TOP or android.view.Gravity.END
        lp.topMargin = 8
        lp.marginEnd = 8
        root.addView(gear, lp)
        setContentView(root)

        // same-host links load in place, so Back has somewhere to go; without a
        // callback the dispatcher finishes the activity and the session view is gone.
        onBackPressedDispatcher.addCallback(this, object : OnBackPressedCallback(true) {
            override fun handleOnBackPressed() {
                if (web.canGoBack()) web.goBack() else finish()
            }
        })

        val url = prefs().getString(KEY, "") ?: ""
        if (url.isBlank()) askURL() else load(url)
    }

    private fun prefs() = getSharedPreferences("grok-pane", Context.MODE_PRIVATE)

    private fun askURL() {
        val box = EditText(this).apply {
            hint = "https://host.ts.net"
            setText(prefs().getString(KEY, "") ?: "")
            inputType = android.text.InputType.TYPE_TEXT_VARIATION_URI
        }
        AlertDialog.Builder(this)
            .setTitle(R.string.pane_url)
            .setMessage(R.string.pane_url_help)
            .setView(box)
            .setPositiveButton(android.R.string.ok) { _, _ ->
                val raw = box.text.toString().trim()
                prefs().edit().putString(KEY, raw).apply()
                load(raw)
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun load(raw: String) {
        var s = raw.trim()
        if (s.isEmpty()) return
        if (!s.contains("://")) {
            s = (if (plainHost(hostOf(s))) "http" else "https") + "://" + s
        }
        val scheme = android.net.Uri.parse(s).scheme?.lowercase()
        if (scheme != "http" && scheme != "https") {
            Toast.makeText(this, getString(R.string.bad_scheme), Toast.LENGTH_LONG).show()
            return
        }
        // the network config has to permit cleartext for the LAN case, so this is
        // the only place that can keep plain http off the public internet.
        if (scheme == "http" && !plainHost(hostOf(s.substringAfter("://")))) {
            s = "https://" + s.substringAfter("://")
            Toast.makeText(this, getString(R.string.forced_https), Toast.LENGTH_LONG).show()
        }
        web.loadUrl(s)
    }

    companion object {
        private const val KEY = "pane-url"

        /** Host of an authority-first string ("host:port/path"), lowercased, brackets stripped. */
        fun hostOf(s: String): String {
            var h = s.substringBefore('/').substringBefore('?').substringBefore('#')
            h = h.substringAfterLast('@')
            if (h.startsWith("[")) {
                return h.substringAfter('[').substringBefore(']').lowercase()
            }
            return h.substringBefore(':').lowercase()
        }

        /**
         * True for hosts pane can serve without TLS: it speaks cleartext, and https
         * only exists where a tailscale-serve front end terminates it, i.e. on the
         * MagicDNS name — never on a loopback or LAN address.
         */
        fun plainHost(host: String): Boolean {
            if (host.isEmpty()) return false
            if (host == "localhost" || host.endsWith(".localhost") || host.endsWith(".local")) return true
            if (host.contains(":")) {
                return host == "::1" || host.startsWith("fe80:") ||
                    host.startsWith("fc") || host.startsWith("fd")
            }
            val octets = ipv4(host)
            if (octets != null) {
                return octets[0] == 127 || octets[0] == 10 ||
                    (octets[0] == 172 && octets[1] in 16..31) ||
                    (octets[0] == 192 && octets[1] == 168) ||
                    (octets[0] == 169 && octets[1] == 254) ||
                    (octets[0] == 100 && octets[1] in 64..127)
            }
            // an unqualified name is a LAN or tailnet short name, not a public host.
            return !host.contains(".")
        }

        private fun ipv4(host: String): IntArray? {
            val parts = host.split(".")
            if (parts.size != 4) return null
            val out = IntArray(4)
            for (i in 0..3) {
                val n = parts[i].toIntOrNull() ?: return null
                if (n < 0 || n > 255) return null
                out[i] = n
            }
            return out
        }
    }
}
