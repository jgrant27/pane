package web

import "embed"

//go:embed index.html style.css term.js favicon.png vendor fonts
var FS embed.FS
