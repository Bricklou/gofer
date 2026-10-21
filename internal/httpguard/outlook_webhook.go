package httpguard

import (
	"errors"
	"net/netip"
)

// OutlookWebhookPath receives Outlook change notifications from Microsoft
// Graph when GOFER_OUTLOOK_WEBHOOKS is enabled. Microsoft's servers send no
// session or origin, so the endpoint skips those checks and accepts only
// notifications that carry a subscription's own secret. GOFER_ALLOWED_CIDRS
// still applies unless GOFER_OUTLOOK_WEBHOOKS_SKIP_ALLOWED_CIDRS is set.
const OutlookWebhookPath = "/webhooks/outlook"

// OutlookWebhookURL is where Graph sends notifications, or empty when
// GOFER_OUTLOOK_WEBHOOKS is off.
func (c *Config) OutlookWebhookURL() string {
	if !c.OutlookWebhooks {
		return ""
	}
	return c.BaseURL + OutlookWebhookPath
}

func (c *Config) isOutlookWebhook(path string) bool {
	return c.OutlookWebhooks && path == OutlookWebhookPath
}

// WarnOutlookWebhooksBehindAllowlist reports whether GOFER_ALLOWED_CIDRS may
// turn away Microsoft's servers, because skipping it wasn't confirmed.
func (c *Config) WarnOutlookWebhooksBehindAllowlist() bool {
	return c.OutlookWebhooks && len(c.allowedCIDRs) > 0 && !c.OutlookWebhooksSkipAllowedCIDRs
}

// validateOutlookWebhooks checks that Microsoft could reach the endpoint at
// all: Graph only sends to public HTTPS addresses.
func (c *Config) validateOutlookWebhooks(authEnabled bool) error {
	if !c.OutlookWebhooks {
		if c.OutlookWebhooksSkipAllowedCIDRs {
			return errors.New("GOFER_OUTLOOK_WEBHOOKS_SKIP_ALLOWED_CIDRS requires GOFER_OUTLOOK_WEBHOOKS=true")
		}
		return nil
	}
	if !authEnabled {
		return errors.New("GOFER_OUTLOOK_WEBHOOKS requires personal or managed authentication")
	}
	public := c.baseOrigin.scheme == "https" && !c.baseLoopback
	if ip, err := netip.ParseAddr(normalizeHost(c.baseOrigin.host)); err == nil {
		public = public && ip.IsGlobalUnicast() && !ip.IsPrivate()
	}
	if !public {
		return errors.New("GOFER_OUTLOOK_WEBHOOKS requires GOFER_BASE_URL to be a public https address")
	}
	return nil
}
