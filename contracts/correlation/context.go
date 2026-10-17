package correlation

import "context"

type key struct{}

// WithID carries a bounded, printable request identifier across adapters.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key{}, id)
}

func ID(ctx context.Context) string {
	id, _ := ctx.Value(key{}).(string)
	return id
}

func Valid(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, ch := range id {
		if ch < 33 || ch > 126 {
			return false
		}
	}
	return true
}
