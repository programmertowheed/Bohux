// Package notify posts deployment outcomes to a project's notification channels
// (ADR-0038). A channel's URL is a credential: it lives only in OpenBao.
package notify

import "github.com/google/uuid"

// URLPath is where a channel's URL lives in OpenBao, apart from App secrets so no App
// can reference it.
func URLPath(tenant, channel uuid.UUID) string {
	return "bohux/" + tenant.String() + "/notifications/" + channel.String() + "/url"
}

// SigningPath is a WEBHOOK channel's signing secret.
func SigningPath(tenant, channel uuid.UUID) string {
	return "bohux/" + tenant.String() + "/notifications/" + channel.String() + "/signing"
}
