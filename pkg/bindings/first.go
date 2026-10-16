// SPDX-License-Identifier: AGPL-3.0-only

package bindings

// First returns the first element of xs for which ok reports true. It is the
// shared loop behind every single-provider binding picker.
func First[T any](xs []T, ok func(T) bool) (T, bool) {
	var zero T
	for _, x := range xs {
		if ok(x) {
			return x, true
		}
	}
	return zero, false
}
