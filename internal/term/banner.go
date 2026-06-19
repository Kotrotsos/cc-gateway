package term

import "fmt"

// Banner prints the startup banner: how to point Claude Code at the gateway, the
// web UI URL (when enabled), and the live hotkeys (when interactive).
func (con *Console) Banner(proxyURL, upstream, uiURL string, keys bool) {
	b := con.color(cBold)
	r := con.color(cReset)
	cy := con.color(cCyan)
	dim := con.color(cDim)

	fmt.Printf(`
%s  cc-gateway %s  Claude Code traffic monitor + trace explorer
%s  forwarding %s  →  %s%s
`, b, r, dim, proxyURL, upstream, r)

	if uiURL != "" {
		fmt.Printf("%s  web UI     %s  %s%s%s\n", dim, r, cy, uiURL, r)
	}

	fmt.Printf(`
%sPoint Claude Code at the gateway (any one of these):%s

  %sexport ANTHROPIC_BASE_URL=%s%s
  claude

%sor for a single run:%s

  %sANTHROPIC_BASE_URL=%s claude%s
`,
		b, r,
		cy, proxyURL, r,
		b, r,
		cy, proxyURL, r,
	)

	if keys {
		fmt.Printf(`
%shotkeys:%s  %ss%s show formatted messages on screen (toggle)   %sf%s record raw JSON to a file (toggle)
`, b, r, cy, r, cy, r)
	}

	fmt.Printf("\n%s──────────────────────────────────────────────────────────────────────%s\n\n", dim, r)
}
