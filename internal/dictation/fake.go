package dictation

import (
	"sync"
	"time"

	"github.com/egoist/mygo/transfer"
)

// Fake is a scriptable System and Clipboard for tests: it records what
// dictation did (activations, pastes, sounds) and lets the test press and
// release the shortcut.
type Fake struct {
	mu sync.Mutex

	PID        int
	Front      Target
	Trusted    bool
	Mic        Permission
	MicAnswer  bool  // RequestMicrophone's answer
	ActivateOK bool  // Activate succeeds
	PasteErr   error // Paste fails with it
	Changes    int   // the clipboard's change count
	Clip       transfer.Data
	Activated  []Target
	Pastes     []string // the clipboard's text at each paste
	Sounds     []string
	Prompted   int
	released   []func()
	// OnPaste runs at each paste (a test changes the clipboard "meanwhile").
	OnPaste func()
}

// NewFake is a Fake whose pastes work, with AudioSmith as pid 1 and
// Notes (pid 42) in front.
func NewFake() *Fake {
	return &Fake{PID: 1, Front: Target{PID: 42, BundleID: "com.apple.Notes", Name: "Notes"}, Trusted: true, ActivateOK: true,
		Mic: PermissionGranted, MicAnswer: true}
}

func (f *Fake) Self() int { f.mu.Lock(); defer f.mu.Unlock(); return f.PID }

func (f *Fake) Frontmost() Target { f.mu.Lock(); defer f.mu.Unlock(); return f.Front }

func (f *Fake) Activate(t Target, _ time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Activated = append(f.Activated, t)
	if f.ActivateOK {
		f.Front = t
	}
	return f.ActivateOK
}

func (f *Fake) Paste() error {
	f.mu.Lock()
	if f.PasteErr != nil {
		defer f.mu.Unlock()
		return f.PasteErr
	}
	text, _ := f.Clip.Read(transfer.Text)
	f.Pastes = append(f.Pastes, string(text))
	hook := f.OnPaste
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil
}

func (f *Fake) AccessibilityTrusted() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.Trusted }
func (f *Fake) PromptAccessibility()       { f.mu.Lock(); f.Prompted++; f.mu.Unlock() }
func (f *Fake) Microphone() Permission     { f.mu.Lock(); defer f.mu.Unlock(); return f.Mic }

func (f *Fake) RequestMicrophone(done func(bool)) {
	f.mu.Lock()
	ok := f.MicAnswer
	if ok {
		f.Mic = PermissionGranted
	} else {
		f.Mic = PermissionDenied
	}
	f.mu.Unlock()
	done(ok)
}

func (f *Fake) ClipboardChangeCount() int { f.mu.Lock(); defer f.mu.Unlock(); return f.Changes }

func (f *Fake) OnHotkeyReleased(fn func()) error {
	f.mu.Lock()
	f.released = append(f.released, fn)
	f.mu.Unlock()
	return nil
}

// Release plays the shortcut's key going up.
func (f *Fake) Release() {
	f.mu.Lock()
	fns := f.released
	f.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

func (f *Fake) PlaySound(name string) { f.mu.Lock(); f.Sounds = append(f.Sounds, name); f.mu.Unlock() }

// Read and Write make Fake a Clipboard too; Write counts a change.
func (f *Fake) Read() (transfer.Data, error) { f.mu.Lock(); defer f.mu.Unlock(); return f.Clip, nil }

func (f *Fake) Write(d transfer.Data) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Clip = d.Snapshot()
	f.Changes++
	return nil
}

// ClipText is the clipboard's text ("" when it has none).
func (f *Fake) ClipText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := f.Clip.Read(transfer.Text)
	return string(b)
}
