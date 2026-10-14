package main

// Settings → Permissions: the macOS permissions AudioSmith uses, their
// state, and the way to grant them. Microphone (recording, dictation)
// and Accessibility (dictation typing into other apps). They are read
// again when AudioSmith comes to the front, and with Check again.

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/dictation"
	"github.com/hehehai/AudioSmith/internal/i18n"
)

// System Settings panes.
const (
	privacyMicrophone    = "x-apple.systempreferences:com.apple.preference.security?Privacy_Microphone"
	privacyAccessibility = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
)

// permissionsState is what the page last read.
type permissionsState struct {
	mic     dictation.Permission
	access  bool
	checked bool
}

// recheckPermissions reads the permissions (cheap system calls).
func (a *app) recheckPermissions() {
	if a.dictSys == nil {
		return
	}
	a.perms = permissionsState{mic: a.dictSys.Microphone(), access: a.dictSys.AccessibilityTrusted(), checked: true}
}

// openPrivacy opens a pane of System Settings.
func (a *app) openPrivacy(c *ui.Context, url string) {
	if a.win == nil {
		c.Services().OpenURL(url)
		return
	}
	go mygo.Shell.OpenExternal(url)
}

func permissionText(p dictation.Permission) (string, bool) {
	switch p {
	case dictation.PermissionGranted:
		return i18n.T("Allowed"), true
	case dictation.PermissionDenied:
		return i18n.T("Not allowed"), false
	case dictation.PermissionRestricted:
		return i18n.T("Restricted by your organisation"), false
	case dictation.PermissionNotDetermined:
		return i18n.T("Not asked yet"), false
	}
	return i18n.T("Unknown"), false
}

func (a *app) permissionsSettings(c *ui.Context) {
	t := c.Theme()
	if !a.perms.checked {
		a.recheckPermissions()
	}
	ui.Text(c, i18n.T("AudioSmith works on this Mac: these permissions are used only for what each row says, and audio never leaves it.")).
		FontSize(px(t, 12)).TextColor(t.TextMuted)
	badge := func(text string, ok bool) {
		color := t.Warning
		if ok {
			color = t.Success
		}
		ui.Text(c, text).FontSize(px(t, 12)).Bold().TextColor(color)
	}
	micText, micOK := permissionText(a.perms.mic)
	settingRow(c, "perm-mic", i18n.T("Microphone"), i18n.T("Recording voices, transcribing recordings, and dictation."), func() {
		badge(micText, micOK)
		if a.perms.mic == dictation.PermissionNotDetermined {
			if ui.Button(c.Key("perm-mic-ask"), i18n.T("Ask")).Clicked() {
				sys := a.dictSys
				a.async(func() {
					sys.RequestMicrophone(func(bool) { a.post(a.recheckPermissions) })
				})
			}
		}
		if !micOK {
			if ui.Button(c.Key("perm-mic-open"), i18n.T("Open System Settings")).Clicked() {
				a.openPrivacy(c, privacyMicrophone)
			}
		}
	})
	accText, accOK := i18n.T("Not allowed"), a.perms.access
	if accOK {
		accText = i18n.T("Allowed")
	}
	settingRow(c, "perm-access", i18n.T("Accessibility"), i18n.T("Lets dictation type the text into other apps (⌘V). Without it the text is copied and you paste it."), func() {
		badge(accText, accOK)
		if !accOK {
			if ui.Button(c.Key("perm-access-ask"), i18n.T("Ask")).Tooltip(i18n.T("Adds AudioSmith to the list in System Settings")).Clicked() {
				a.dictSys.PromptAccessibility()
				a.recheckPermissions()
			}
			if ui.Button(c.Key("perm-access-open"), i18n.T("Open System Settings")).Clicked() {
				a.openPrivacy(c, privacyAccessibility)
			}
		}
	})
	ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
		if ui.Button(c.Key("perm-recheck"), i18n.T("Check again")).Clicked() {
			a.recheckPermissions()
		}
		ui.Text(c, i18n.T("After you change a permission, macOS may ask to quit and reopen AudioSmith. Global shortcuts need no permission.")).
			FontSize(px(t, 11)).TextColor(t.TextMuted)
	})
}
