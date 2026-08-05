// Package state defines a small, binary-safe String and TTL state engine.
// Protocol adapters such as Redis depend on these semantics rather than on a
// concrete persistence backend.
package state
