// operations.go defines the minimal Twitch GraphQL operations used by the app.
// It builds either persisted-query or inline query requests for viewer identity
// and streamer login resolution without exposing raw payload assembly to callers.
package gql

// persistedQuery stores the persisted-query metadata Twitch expects.
type persistedQuery struct {
	Version    int    `json:"version"`
	SHA256Hash string `json:"sha256Hash"`
}

// extensions holds the GraphQL extensions block for one request.
type extensions struct {
	PersistedQuery *persistedQuery `json:"persistedQuery,omitempty"`
}

// Request is one Twitch GraphQL operation.
type Request struct {
	OperationName string         `json:"operationName,omitempty"`
	Query         string         `json:"query,omitempty"`
	Variables     map[string]any `json:"variables,omitempty"`
	Extensions    extensions     `json:"extensions,omitempty"`
}

// operation builds a persisted-query request with the supplied variables.
func operation(name, hash string, variables map[string]any) Request {
	return Request{
		OperationName: name,
		Variables:     variables,
		Extensions: extensions{
			PersistedQuery: &persistedQuery{
				Version:    1,
				SHA256Hash: hash,
			},
		},
	}
}

// queryOperation builds an inline-query request with the supplied variables.
func queryOperation(name, query string, variables map[string]any) Request {
	return Request{
		OperationName: name,
		Query:         query,
		Variables:     variables,
	}
}

// operationLabel returns a readable operation name for logs and errors.
func (r Request) operationLabel() string {
	if r.OperationName != "" {
		return r.OperationName
	}
	if r.Query != "" {
		return "anonymous"
	}
	return "unknown"
}

// CurrentUser fetches the canonical identity for the authenticated viewer.
func CurrentUser() Request {
	return queryOperation("CurrentUser", "query CurrentUser { currentUser { id login } }", nil)
}

// GetIDFromLogin resolves one login into a Twitch user record using Twitch's persisted-query hash.
func GetIDFromLogin(login string) Request {
	return operation("GetIDFromLogin", "94e82a7b1e3c21e186daa73ee2afc4b8f23bade1fbbff6fe8ac133f50a2f58ca", map[string]any{
		"login": login,
	})
}
