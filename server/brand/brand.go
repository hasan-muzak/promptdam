// Package brand holds every name Promptdam shows the outside world: the
// product's name and key, its API key prefix, its domain and the headers the
// gateway reads and writes. The product will be renamed; renaming it changes
// this file, the PROMPTDAM_* variables in core and the folder, nothing else.
//
// It imports nothing from the product, so every package may use it.
package brand

// Names.
const (
	// Name is shown to people.
	Name = "Promptdam"
	// Key namespaces Valkey keys, Stripe objects, migrations and meters.
	Key = "promptdam"
	// KeyPrefix starts API keys: pd_sk_live_….
	KeyPrefix = "pd"
	// Domain is the product's site; the gateway is gateway.<Domain>.
	Domain = "promptdam.dev"
	// SupportEmail receives replies to emails.
	SupportEmail = "support@" + Domain
)

// HeaderPrefix starts every header the gateway reads or writes.
const HeaderPrefix = "X-Promptdam-"

// Request headers: attribution a client may send with any gateway request.
const (
	// HeaderUser names the end user or developer a request is for.
	HeaderUser = HeaderPrefix + "User"
	// HeaderRoute picks a route by name.
	HeaderRoute = HeaderPrefix + "Route"
	// HeaderTags lists tags, comma-separated.
	HeaderTags = HeaderPrefix + "Tags"
	// HeaderFamily names the call's prompt family (its task), instead of the
	// family Promptdam derives from its instructions.
	HeaderFamily = HeaderPrefix + "Family"
	// HeaderTier picks a model router's tier for the call, as
	// "model": "<router>:<tier>" does. The response names the tier that
	// served it.
	HeaderTier = HeaderPrefix + "Tier"
	// HeaderSession names the conversation a call belongs to, for clients
	// that keep one; others' conversations are told apart by their
	// messages.
	HeaderSession = HeaderPrefix + "Session"
)

// Response headers the gateway adds.
const (
	// HeaderTrace identifies the request in logs and support.
	HeaderTrace = HeaderPrefix + "Trace"
	// HeaderServedModel is the model that answered, which may differ from
	// the requested one after a fallback.
	HeaderServedModel = HeaderPrefix + "Served-Model"
	// HeaderProvider is the provider that answered.
	HeaderProvider = HeaderPrefix + "Provider"
	// HeaderRouter names the model router that placed the call, and
	// HeaderTierSource the step that chose its tier (explicit, rule,
	// classifier, decision, default; "shadow:" first when the router only
	// watched).
	HeaderRouter     = HeaderPrefix + "Router"
	HeaderTierSource = HeaderPrefix + "Tier-Source"
	// HeaderCache says how the response was cached: hit, miss or prompt.
	HeaderCache = HeaderPrefix + "Cache"
	// HeaderErrorCode is the stable code of an error the gateway itself
	// answered with (not the provider).
	HeaderErrorCode = HeaderPrefix + "Error-Code"
	// HeaderDropped lists, comma-separated, the request's parameters a
	// provider of the other API format had no equivalent for (seed, top_k…).
	HeaderDropped = HeaderPrefix + "Dropped"
)
