//go:build darwin

package dictation

// The macOS side, through purego (as MyGo's internal/darwin does): no
// cgo. Frameworks are opened at the first use; the Carbon callback is
// made once.

import (
	"os"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const (
	rtld = purego.RTLD_GLOBAL | purego.RTLD_NOW

	// Carbon.
	kEventClassKeyboard     = 0x6B657962 // 'keyb'
	kEventHotKeyReleased    = 6
	kEventParamDirectObject = 0x2D2D2D2D // '----'
	typeEventHotKeyID       = 0x686B6964 // 'hkid'
	mygoHotKeySignature     = 0x4D79476F // 'MyGo': mygo.GlobalShortcut's hot keys

	// CoreGraphics.
	kCGEventSourceStateHIDSystemState = 1
	kCGHIDEventTap                    = 0
	kCGEventFlagMaskCommand           = 0x00100000

	// UCKeyTranslate.
	kUCKeyActionDisplay        = 3
	kUCKeyTranslateNoDeadKeys  = 1
	cmdKeyModifierState        = 1 // (cmdKey >> 8) & 0xFF
	ansiV                      = 9
	nsActivateIgnoringOtherApp = 1 << 1
)

type eventHotKeyID struct {
	Signature uint32
	ID        uint32
}

type eventTypeSpec struct {
	Class uint32
	Kind  uint32
}

var mac struct {
	once sync.Once
	err  error

	// libobjc
	poolPush func() uintptr
	poolPop  func(uintptr)
	// CoreFoundation
	cfRelease        func(uintptr)
	cfDataGetBytePtr func(uintptr) uintptr
	// CoreGraphics
	cgEventSourceCreate        func(state int32) uintptr
	cgEventCreateKeyboardEvent func(src uintptr, key uint16, down bool) uintptr
	cgEventSetFlags            func(ev uintptr, flags uint64)
	cgEventPost                func(tap uint32, ev uintptr)
	// HIToolbox / Carbon
	tisCopyCurrentKeyboardLayoutInputSource func() uintptr
	tisCopyCurrentASCIICapableLayout        func() uintptr
	tisGetInputSourceProperty               func(src, key uintptr) uintptr
	lmGetKbdType                            func() uint8
	ucKeyTranslate                          func(layout uintptr, key, action uint16, mods, kbdType, options uint32, dead *uint32, max uint64, actual *uint64, chars *uint16) int32
	kTISPropertyUnicodeKeyLayoutData        uintptr
	getApplicationEventTarget               func() uintptr
	installEventHandler                     func(target, handler uintptr, numTypes uint32, list *eventTypeSpec, userData uintptr, outRef *uintptr) int32
	getEventParameter                       func(event uintptr, name, typ uint32, outType uintptr, size uintptr, outSize uintptr, data unsafe.Pointer) int32
	// HIServices (ApplicationServices)
	axIsProcessTrusted            func() bool
	axIsProcessTrustedWithOptions func(options uintptr) bool
	kAXTrustedCheckOptionPrompt   uintptr
	// AVFoundation
	avMediaTypeAudio uintptr
}

func load() error {
	mac.once.Do(func() {
		libs := map[string]uintptr{}
		for name, path := range map[string]string{
			"objc":   "/usr/lib/libobjc.A.dylib",
			"cf":     "/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
			"cg":     "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics",
			"carbon": "/System/Library/Frameworks/Carbon.framework/Carbon",
			"appsvc": "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices",
			"appkit": "/System/Library/Frameworks/AppKit.framework/AppKit",
			"av":     "/System/Library/Frameworks/AVFoundation.framework/AVFoundation",
		} {
			h, err := purego.Dlopen(path, rtld)
			if err != nil {
				mac.err = err
				return
			}
			libs[name] = h
		}
		purego.RegisterLibFunc(&mac.poolPush, libs["objc"], "objc_autoreleasePoolPush")
		purego.RegisterLibFunc(&mac.poolPop, libs["objc"], "objc_autoreleasePoolPop")
		purego.RegisterLibFunc(&mac.cfRelease, libs["cf"], "CFRelease")
		purego.RegisterLibFunc(&mac.cfDataGetBytePtr, libs["cf"], "CFDataGetBytePtr")
		purego.RegisterLibFunc(&mac.cgEventSourceCreate, libs["cg"], "CGEventSourceCreate")
		purego.RegisterLibFunc(&mac.cgEventCreateKeyboardEvent, libs["cg"], "CGEventCreateKeyboardEvent")
		purego.RegisterLibFunc(&mac.cgEventSetFlags, libs["cg"], "CGEventSetFlags")
		purego.RegisterLibFunc(&mac.cgEventPost, libs["cg"], "CGEventPost")
		purego.RegisterLibFunc(&mac.tisCopyCurrentKeyboardLayoutInputSource, libs["carbon"], "TISCopyCurrentKeyboardLayoutInputSource")
		purego.RegisterLibFunc(&mac.tisCopyCurrentASCIICapableLayout, libs["carbon"], "TISCopyCurrentASCIICapableKeyboardLayoutInputSource")
		purego.RegisterLibFunc(&mac.tisGetInputSourceProperty, libs["carbon"], "TISGetInputSourceProperty")
		purego.RegisterLibFunc(&mac.lmGetKbdType, libs["carbon"], "LMGetKbdType")
		purego.RegisterLibFunc(&mac.ucKeyTranslate, libs["carbon"], "UCKeyTranslate")
		purego.RegisterLibFunc(&mac.getApplicationEventTarget, libs["carbon"], "GetApplicationEventTarget")
		purego.RegisterLibFunc(&mac.installEventHandler, libs["carbon"], "InstallEventHandler")
		purego.RegisterLibFunc(&mac.getEventParameter, libs["carbon"], "GetEventParameter")
		purego.RegisterLibFunc(&mac.axIsProcessTrusted, libs["appsvc"], "AXIsProcessTrusted")
		purego.RegisterLibFunc(&mac.axIsProcessTrustedWithOptions, libs["appsvc"], "AXIsProcessTrustedWithOptions")
		mac.kTISPropertyUnicodeKeyLayoutData = constant(libs["carbon"], "kTISPropertyUnicodeKeyLayoutData")
		mac.kAXTrustedCheckOptionPrompt = constant(libs["appsvc"], "kAXTrustedCheckOptionPrompt")
		mac.avMediaTypeAudio = constant(libs["av"], "AVMediaTypeAudio")
	})
	return mac.err
}

// constant reads an exported object constant (a CFStringRef / NSString *).
func constant(lib uintptr, name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil || p == 0 {
		return 0
	}
	return **(**uintptr)(unsafe.Pointer(&p))
}

// pooled runs fn inside an autorelease pool: AppKit's autoreleased
// objects made on a goroutine's thread are freed at its end.
func pooled(fn func()) {
	p := mac.poolPush()
	defer mac.poolPop(p)
	fn()
}

var (
	selShared       = objc.RegisterName("sharedWorkspace")
	selFrontmost    = objc.RegisterName("frontmostApplication")
	selPID          = objc.RegisterName("processIdentifier")
	selBundleID     = objc.RegisterName("bundleIdentifier")
	selLocalized    = objc.RegisterName("localizedName")
	selUTF8         = objc.RegisterName("UTF8String")
	selRunningByPID = objc.RegisterName("runningApplicationWithProcessIdentifier:")
	selActivate     = objc.RegisterName("activateWithOptions:")
	selSharedApp    = objc.RegisterName("sharedApplication")
	selIsActive     = objc.RegisterName("isActive")
	selYield        = objc.RegisterName("yieldActivationToApplication:")
	selResponds     = objc.RegisterName("respondsToSelector:")
	selGeneralPB    = objc.RegisterName("generalPasteboard")
	selChangeCount  = objc.RegisterName("changeCount")
	selAuthStatus   = objc.RegisterName("authorizationStatusForMediaType:")
	selRequest      = objc.RegisterName("requestAccessForMediaType:completionHandler:")
	selSoundNamed   = objc.RegisterName("soundNamed:")
	selPlay         = objc.RegisterName("play")
	selNumberBool   = objc.RegisterName("numberWithBool:")
	selDictWith     = objc.RegisterName("dictionaryWithObject:forKey:")
	selNSString     = objc.RegisterName("stringWithUTF8String:")
)

func goString(s objc.ID) string {
	if s == 0 {
		return ""
	}
	p := objc.Send[*byte](s, selUTF8)
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

func nsString(s string) objc.ID {
	b := append([]byte(s), 0)
	return objc.ID(objc.GetClass("NSString")).Send(selNSString, &b[0])
}

type native struct{}

// Native is the macOS implementation.
func Native() System { return native{} }

func (native) Self() int { return os.Getpid() }

func (native) Frontmost() Target {
	if load() != nil {
		return Target{}
	}
	var t Target
	pooled(func() {
		ws := objc.ID(objc.GetClass("NSWorkspace")).Send(selShared)
		app := ws.Send(selFrontmost)
		if app == 0 {
			return
		}
		t.PID = int(objc.Send[int32](app, selPID))
		t.BundleID = goString(app.Send(selBundleID))
		t.Name = goString(app.Send(selLocalized))
	})
	return t
}

func frontPID() int {
	pid := 0
	pooled(func() {
		ws := objc.ID(objc.GetClass("NSWorkspace")).Send(selShared)
		if app := ws.Send(selFrontmost); app != 0 {
			pid = int(objc.Send[int32](app, selPID))
		}
	})
	return pid
}

func (native) Activate(t Target, wait time.Duration) bool {
	if load() != nil || !t.Valid() {
		return false
	}
	if frontPID() == t.PID {
		return true
	}
	pooled(func() {
		app := objc.ID(objc.GetClass("NSRunningApplication")).Send(selRunningByPID, int32(t.PID))
		if app == 0 {
			return
		}
		// macOS 14 activates cooperatively: an active app yields first.
		nsApp := objc.ID(objc.GetClass("NSApplication")).Send(selSharedApp)
		if nsApp != 0 && objc.Send[bool](nsApp, selIsActive) && objc.Send[bool](nsApp, selResponds, selYield) {
			nsApp.Send(selYield, app)
		}
		objc.Send[bool](app, selActivate, uint64(nsActivateIgnoringOtherApp))
	})
	deadline := time.Now().Add(wait)
	for {
		if frontPID() == t.PID {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pasteKey is the key that types "v" with ⌘ in the current layout (ANSI
// V elsewhere): Dvorak's ⌘V is not on the V key of a US keyboard.
func pasteKey() uint16 {
	key := uint16(ansiV)
	for _, copySrc := range []func() uintptr{mac.tisCopyCurrentKeyboardLayoutInputSource, mac.tisCopyCurrentASCIICapableLayout} {
		src := copySrc()
		if src == 0 {
			continue
		}
		data := uintptr(0)
		if mac.kTISPropertyUnicodeKeyLayoutData != 0 {
			data = mac.tisGetInputSourceProperty(src, mac.kTISPropertyUnicodeKeyLayoutData)
		}
		found := false
		if data != 0 {
			layout := mac.cfDataGetBytePtr(data)
			kbd := uint32(mac.lmGetKbdType())
			for k := uint16(0); k < 128 && !found; k++ {
				var dead uint32
				var n uint64
				var chars [4]uint16
				if mac.ucKeyTranslate(layout, k, kUCKeyActionDisplay, cmdKeyModifierState, kbd, kUCKeyTranslateNoDeadKeys, &dead, 4, &n, &chars[0]) == 0 &&
					n == 1 && (chars[0] == 'v' || chars[0] == 'V') {
					key, found = k, true
				}
			}
		}
		mac.cfRelease(src)
		if found || data != 0 {
			// A layout with key data decided (input methods such as
			// Pinyin have none: their ASCII-capable layout decides).
			break
		}
	}
	return key
}

func (native) Paste() error {
	if err := load(); err != nil {
		return err
	}
	key := pasteKey()
	src := mac.cgEventSourceCreate(kCGEventSourceStateHIDSystemState)
	down := mac.cgEventCreateKeyboardEvent(src, key, true)
	up := mac.cgEventCreateKeyboardEvent(src, key, false)
	if down == 0 || up == 0 {
		return ErrUnsupported
	}
	mac.cgEventSetFlags(down, kCGEventFlagMaskCommand)
	mac.cgEventSetFlags(up, kCGEventFlagMaskCommand)
	mac.cgEventPost(kCGHIDEventTap, down)
	mac.cgEventPost(kCGHIDEventTap, up)
	mac.cfRelease(down)
	mac.cfRelease(up)
	if src != 0 {
		mac.cfRelease(src)
	}
	return nil
}

func (native) AccessibilityTrusted() bool {
	return load() == nil && mac.axIsProcessTrusted()
}

func (native) PromptAccessibility() {
	if load() != nil || mac.kAXTrustedCheckOptionPrompt == 0 {
		return
	}
	pooled(func() {
		yes := objc.ID(objc.GetClass("NSNumber")).Send(selNumberBool, true)
		opts := objc.ID(objc.GetClass("NSDictionary")).Send(selDictWith, yes, mac.kAXTrustedCheckOptionPrompt)
		mac.axIsProcessTrustedWithOptions(uintptr(opts))
	})
}

func (native) Microphone() Permission {
	if load() != nil || mac.avMediaTypeAudio == 0 {
		return PermissionUnknown
	}
	var st int
	pooled(func() {
		st = int(objc.Send[int](objc.ID(objc.GetClass("AVCaptureDevice")), selAuthStatus, mac.avMediaTypeAudio))
	})
	switch st {
	case 0:
		return PermissionNotDetermined
	case 1:
		return PermissionRestricted
	case 2:
		return PermissionDenied
	case 3:
		return PermissionGranted
	}
	return PermissionUnknown
}

// micBlock is the completion handler of requestAccessForMediaType: one
// block type, so purego makes its callback once.
var (
	micMu   sync.Mutex
	micDone []func(bool)
)

func (native) RequestMicrophone(done func(bool)) {
	if load() != nil || mac.avMediaTypeAudio == 0 {
		go done(false)
		return
	}
	micMu.Lock()
	micDone = append(micDone, done)
	micMu.Unlock()
	block := objc.NewBlock(func(_ objc.Block, granted bool) {
		micMu.Lock()
		fns := micDone
		micDone = nil
		micMu.Unlock()
		for _, fn := range fns {
			go fn(granted)
		}
	})
	pooled(func() {
		objc.ID(objc.GetClass("AVCaptureDevice")).Send(selRequest, mac.avMediaTypeAudio, block)
	})
	block.Release()
}

func (native) ClipboardChangeCount() int {
	if load() != nil {
		return -1
	}
	n := -1
	pooled(func() {
		pb := objc.ID(objc.GetClass("NSPasteboard")).Send(selGeneralPB)
		if pb != 0 {
			n = int(objc.Send[int](pb, selChangeCount))
		}
	})
	return n
}

var (
	releaseMu  sync.Mutex
	releaseFns []func()
	// releasedCallback is the Carbon handler for kEventHotKeyReleased;
	// MyGo's own handler takes only presses. Made once.
	releasedCallback = sync.OnceValue(func() uintptr {
		return purego.NewCallback(func(next, event, userData uintptr) uintptr {
			var hk eventHotKeyID
			if mac.getEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, 0, unsafe.Sizeof(hk), 0, unsafe.Pointer(&hk)) == 0 &&
				hk.Signature == mygoHotKeySignature {
				releaseMu.Lock()
				fns := releaseFns
				releaseMu.Unlock()
				for _, fn := range fns {
					fn()
				}
			}
			return 0
		})
	})
	installOnce sync.Once
	installErr  error
)

func (native) OnHotkeyReleased(fn func()) error {
	if err := load(); err != nil {
		return err
	}
	installOnce.Do(func() {
		spec := eventTypeSpec{Class: kEventClassKeyboard, Kind: kEventHotKeyReleased}
		var ref uintptr
		if st := mac.installEventHandler(mac.getApplicationEventTarget(), releasedCallback(), 1, &spec, 0, &ref); st != 0 {
			installErr = ErrUnsupported
		}
	})
	if installErr != nil {
		return installErr
	}
	releaseMu.Lock()
	releaseFns = append(releaseFns, fn)
	releaseMu.Unlock()
	return nil
}

func (native) PlaySound(name string) {
	if load() != nil {
		return
	}
	pooled(func() {
		snd := objc.ID(objc.GetClass("NSSound")).Send(selSoundNamed, nsString(name))
		if snd != 0 {
			snd.Send(selPlay)
		}
	})
}
