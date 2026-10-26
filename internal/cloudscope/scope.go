package cloudscope

import "context"

type key struct{}

func With(ctx context.Context, scope string) context.Context {
	return context.WithValue(ctx, key{}, scope)
}
func From(ctx context.Context) (string, bool) {
	scope, ok := ctx.Value(key{}).(string)
	return scope, ok
}
