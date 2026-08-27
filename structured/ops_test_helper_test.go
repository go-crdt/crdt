package structured

// must is what a test says to a reader that can refuse; see the one in the
// crdt package, which says why.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
