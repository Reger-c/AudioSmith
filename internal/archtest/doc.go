// Package archtest holds no code, only tests of the code's structure, run
// with every go test ./... (docs/architecture.md, "Code structure rules"):
//
//   - TestFileSizes: a Go file has at most 600 lines, a test file 800.
//     Files over it today are in allowlist.txt with a ceiling they may not
//     grow past (a ratchet); split them and remove their lines.
//     Generated files (// Code generated … DO NOT EDIT.), third_party,
//     testdata and the Swift engine are not counted.
//   - TestImports: the layering of internal/ (imports_test.go has the rules and
//     why), and every import of one internal package by another listed in
//     imports.txt, so that a new dependency shows in review.
//   - TestLongFuncs: reports (t.Log, go test -v) functions over 150 lines.
//
// Each failure says how to fix it.
package archtest
