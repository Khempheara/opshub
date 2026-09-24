package server

import "net/http"

// swaggerUIVersion pins the Swagger UI assets loaded from jsDelivr.
// TODO(air-gapped installs): vendor swagger-ui-dist into the image instead of a CDN.
const swaggerUIVersion = "5.33.0"

const swaggerHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>OpsHub API</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui-bundle.js"></script>
  <script src="/docs/init.js"></script>
</body>
</html>`

// Kept out of the HTML so the CSP can forbid inline scripts.
const swaggerInitJS = `window.ui = SwaggerUIBundle({ url: "/api/openapi.yaml", dom_id: "#swagger-ui", deepLinking: true });`

const docsCSP = "default-src 'none'; script-src 'self' https://cdn.jsdelivr.net; " +
	"style-src 'self' https://cdn.jsdelivr.net; img-src 'self' data: https://cdn.jsdelivr.net; " +
	"connect-src 'self'; frame-ancestors 'none'"

func swaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Security-Policy", docsCSP)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerHTML))
}

func swaggerInit(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write([]byte(swaggerInitJS))
}
