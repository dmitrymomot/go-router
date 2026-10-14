package i18n

import (
	"context"
	"time"
)

// ctxKey carries the Localizer of [Bundle.WithLocale]. The middleware puts it
// on the request context, so the handler context answers it too:
// router.Base.Value falls back to the request context.
type ctxKey struct{}

// WithLocale returns a copy of ctx that carries a [Localizer] for loc. The
// middleware calls it on each request; code that runs outside a request, such
// as a background job, calls it itself:
//
//	ctx = bundle.WithLocale(ctx, bundle.ParseOrDefault(user.Locale))
//	subject := i18n.T(ctx, "mail.welcome.subject")
func (b *Bundle) WithLocale(ctx context.Context, loc Locale) context.Context {
	return context.WithValue(ctx, ctxKey{}, b.For(loc))
}

// Ctx returns the [Localizer] that ctx carries, or one bound to the default
// locale of b. Unlike [From] it always returns a bound Localizer.
func (b *Bundle) Ctx(ctx context.Context) Localizer {
	if l, ok := lookup(ctx); ok {
		return l
	}
	return b.For(b.Default())
}

// From returns the [Localizer] that ctx carries: the one the middleware put
// on the request, or the one of [Bundle.WithLocale]. Any context of the
// request works: the handler context c, c.Request().Context(), the ctx of a
// templ component, and a context derived from any of them. Without one, From
// returns the zero Localizer, which fails closed: messages echo their keys and
// values take the Invariant format.
func From(ctx context.Context) Localizer {
	l, _ := lookup(ctx)
	return l
}

func lookup(ctx context.Context) (Localizer, bool) {
	l, ok := ctx.Value(ctxKey{}).(Localizer)
	return l, ok
}

// LocaleFrom returns the locale that ctx carries, or the zero Locale.
func LocaleFrom(ctx context.Context) Locale { return From(ctx).Locale() }

// T renders a message in the locale of ctx.
func T(ctx context.Context, key string, args ...any) string {
	return From(ctx).T(key, args...)
}

// TN renders a plural message in the locale of ctx, with n as {{count}}.
func TN(ctx context.Context, key string, n int, args ...any) string {
	return From(ctx).TN(key, n, args...)
}

// TK is T with a declared Key.
func TK(ctx context.Context, k Key, args ...any) string {
	return From(ctx).TK(k, args...)
}

// TNK is TN with a declared Key.
func TNK(ctx context.Context, k Key, n int, args ...any) string {
	return From(ctx).TNK(k, n, args...)
}

// Number formats v in the locale of ctx.
func Number(ctx context.Context, v float64) string { return From(ctx).Number(v) }

// NumberInt formats n in the locale of ctx.
func NumberInt(ctx context.Context, n int64) string { return From(ctx).NumberInt(n) }

// Percent formats a ratio in the locale of ctx.
func Percent(ctx context.Context, ratio float64) string { return From(ctx).Percent(ratio) }

// Currency formats m in the locale of ctx.
func Currency(ctx context.Context, m Money) string { return From(ctx).Currency(m) }

// Date formats the date of t in the locale of ctx.
func Date(ctx context.Context, t time.Time) string { return From(ctx).Date(t) }

// Time formats the clock time of t in the locale of ctx.
func Time(ctx context.Context, t time.Time) string { return From(ctx).Time(t) }

// DateTime formats t in the locale of ctx.
func DateTime(ctx context.Context, t time.Time) string { return From(ctx).DateTime(t) }
