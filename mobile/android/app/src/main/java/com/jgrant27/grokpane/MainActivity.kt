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
                    val host = req.url.host
                    val here = view.url?.let { android.net.Uri.parse(it).host }
                    if (req.url.scheme == "mailto" || (host != null && here != null && host != here)) {
                        startActivity(android.content.Intent(android.content.Intent.ACTION_VIEW, req.url))
                        return true
                    }
                    return false
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
        if (!s.contains("://")) s = "https://$s"
        web.loadUrl(s)
    }

    companion object {
        private const val KEY = "pane-url"
    }
}
