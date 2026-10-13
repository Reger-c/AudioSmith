//go:build !(darwin || linux)

package hub

func freeBytes(string) int64 { return -1 }
