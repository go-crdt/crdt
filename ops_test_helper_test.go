package crdt

// must is what a test says to a reader that can refuse.
//
// [Doc.OpsSince] and its neighbours return an error below the collection floor,
// and a test that has collected nothing can never see it. Saying so once here
// keeps a hundred call sites from saying it a hundred times, and turns the case
// that cannot happen into a failure rather than a silently ignored value.
func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
