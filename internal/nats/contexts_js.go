//go:build js

package nats

// ListContexts reports no NATS CLI contexts in the browser: there is no
// filesystem to read ~/.config/nats from, so the server browser shows only
// favorites.
func ListContexts() (names []string, selected string, err error) {
	return nil, "", nil
}

// ContextURL is always "" in the browser (see ListContexts).
func ContextURL(name string) string { return "" }
