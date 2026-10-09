// A module of its own, so the module above it still requires nothing -- which
// two written decisions depend on, and which TestTheModuleStillRequiresNothing
// holds -- and so that `go build ./...` and the 100% coverage gate never see a
// CI program.
module github.com/go-crdt/crdt/tools

go 1.27.2
