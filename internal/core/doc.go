// Package core holds AudioSmith's domain: the entities every module shares
// (takes, voices, projects, licences, notifications, log lines), the
// service interfaces the UI and the Local API call, and the event bus they
// both subscribe to.
//
// core is a leaf: it imports no other internal package, so every other
// package may import it. Implementations live elsewhere:
//
//	internal/store     persistence (history.json today, SQLite next)
//	internal/services  goroutine-safe implementations of the interfaces below (thread A)
//	internal/jobs      the job scheduler the services submit work to
//	internal/engine    the Swift engine supervisor the scheduler runs work on
//
// Rules for everything in this package:
//
//   - Service methods are safe to call from any goroutine and may block on
//     I/O: never call them from a MyGo build pass. The UI calls them from a
//     goroutine (or an action handler that starts one) and learns about the
//     outcome from the Bus, applying it with Window.Update.
//   - Every service method takes a context.Context first; cancelling it
//     abandons the call, not the work a job already started (use the job's
//     Cancel for that).
//   - Entities are plain values. Services return copies; mutate through the
//     service, never through a value you were handed.
//   - Errors wrap the sentinels in errors.go, so callers test with errors.Is.
package core
