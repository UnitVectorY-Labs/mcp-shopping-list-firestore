
# Commands for mcp-shopping-list-firestore
default:
  @just --list
# Build mcp-shopping-list-firestore with Go
build:
  go build ./...

# Run tests for mcp-shopping-list-firestore with Go
test:
  go clean -testcache
  go test ./...