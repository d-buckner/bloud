package sharing

import "fmt"

// serveConfig is the Tailscale Serve JSON configuration structure.
// See https://github.com/tailscale/tailscale/blob/main/ipn/serve.go
type serveConfig struct {
	TCP map[string]tcpConfig `json:"TCP"`
	Web map[string]webConfig `json:"Web"`
}

type tcpConfig struct {
	HTTPS bool `json:"HTTPS"`
}

type webConfig struct {
	Handlers map[string]handler `json:"Handlers"`
}

type handler struct {
	Proxy string `json:"Proxy"`
}

// buildGatewayServeConfig creates the Tailscale Serve config for both gateway
// and app tailnet node containers, proxying HTTPS on port 443 to Traefik on localhost.
func buildGatewayServeConfig(traefikPort int) serveConfig {
	proxyTarget := fmt.Sprintf("http://localhost:%d", traefikPort)
	return serveConfig{
		TCP: map[string]tcpConfig{
			"443": {HTTPS: true},
		},
		Web: map[string]webConfig{
			"${TS_CERT_DOMAIN}:443": {
				Handlers: map[string]handler{
					"/": {Proxy: proxyTarget},
				},
			},
		},
	}
}
