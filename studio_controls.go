package main

// The composer's controls: the "+ Insert" menu of markup (Alt+/), the
// language picker, and the Audio quality and Voice controls popovers.
// What they offer follows the model: its catalogue capabilities and its
// render traits (services.TraitsFor).

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/hehehai/AudioSmith/internal/catalog"
	"github.com/hehehai/AudioSmith/internal/i18n"
	"github.com/hehehai/AudioSmith/internal/icons"
	"github.com/hehehai/AudioSmith/internal/lang"
	"github.com/hehehai/AudioSmith/internal/services"
	"github.com/hehehai/AudioSmith/internal/text/markup"
)

// composerState is the composer's view state.
type composerState struct {
	insertOpen  bool
	langOpen    bool
	qualityOpen bool
	voiceOpen   bool
	langQuery   string
	langList    ui.ListState
	// undoToast, when set, is shown as a toast with Undo (an import).
	undoToast string
	// panel: the inspector shown or hidden by its toggle; panelAuto
	// while the toggle was not used (shown when the window is wide
	// enough).
	panel, panelSet bool
}

// insertItem is an entry of the insert menu.
type insertItem struct {
	key, label  string
	open, close string // text before and after the selection
	hint        string // what it inserts, as shown; open when empty
	nonVerbal   bool
}

func pauseItems() []insertItem {
	return []insertItem{
		{key: "pause", label: i18n.T("Short pause"), open: "[pause]"},
		{key: "pause-500", label: i18n.T("Half a second"), open: "[pause 500ms]"},
		{key: "pause-1s", label: i18n.T("One second"), open: "[pause 1s]"},
		{key: "pause-2s", label: i18n.T("Two seconds"), open: "[pause 2s]"},
	}
}

func deliveryItems() []insertItem {
	return []insertItem{
		{key: "slow", label: i18n.T("Slower"), open: "[slow]", close: "[/slow]"},
		{key: "fast", label: i18n.T("Faster"), open: "[fast]", close: "[/fast]"},
		{key: "spell", label: i18n.T("Spell out"), open: "[spell]", close: "[/spell]"},
	}
}

// nonVerbalLabel names a non-verbal tag.
func nonVerbalLabel(tag string) string {
	switch tag {
	case "laughter":
		return i18n.T("Laughter")
	case "sigh":
		return i18n.T("Sigh")
	case "breath":
		return i18n.T("Breath")
	case "confirmation-en":
		return i18n.T("Agreement (“mm-hm”)")
	case "dissatisfaction-hnn":
		return i18n.T("Displeasure (“hnn”)")
	}
	if rest, ok := strings.CutPrefix(tag, "question-"); ok {
		return i18n.T("Question (“%s?”)", rest)
	}
	if rest, ok := strings.CutPrefix(tag, "surprise-"); ok {
		return i18n.T("Surprise (“%s!”)", rest)
	}
	return tag
}

// insertMenu is the Insert button and its menu of markup.
func (a *app) insertMenu(c *ui.Context) {
	t := c.Theme()
	k := tok(t)
	tr := a.traits()
	var btn ui.Element
	if a.studioNarrow(c) {
		btn = iconButton(c.Key("insert"), icons.AddCircle, i18n.T("Insert"))
	} else {
		btn = dropButton(c.Key("insert"), icons.AddCircle, i18n.T("Insert"))
	}
	btn.Tooltip(i18n.T("Insert a pause, a delivery change or an expression (⌥/)"))
	if btn.Clicked() {
		a.composer.insertOpen = !a.composer.insertOpen
	}
	ui.Popover(c, btn, &a.composer.insertOpen, func() {
		ui.Column(c.Key("insert-menu")).Width(px(t, 260)).Gap(px(t, 2)).Padding(px(t, 4)).Children(func() {
			section := func(title string) {
				ui.Text(c, title).FontSize(px(t, fsMicro)).FontWeight(600).TextColor(k.Text3).Padding(px(t, sp2), px(t, sp2), px(t, 2))
			}
			item := func(it insertItem, disabled bool) {
				row := ui.ButtonBase(c.Key("ins-"+it.key)).Padding(px(t, 5), px(t, sp2)).Radius(px(t, radiusControl)).Label(it.label).Disabled(disabled)
				if row.Hovered() && !disabled {
					row.Background(k.Hover)
				}
				if row.Clicked() && !disabled {
					a.composer.insertOpen = false
					if it.close != "" {
						a.wrapSelection(it.open, it.close)
					} else {
						a.insertScript(it.open)
					}
				}
				row.Children(func() {
					color := k.Text
					if disabled {
						color = k.Text3
					}
					ui.Text(c, it.label).TextColor(color).Grow(1).SingleLine().Ellipsis("…").MinWidth(0)
					hint := it.hint
					if hint == "" {
						hint = it.open
					}
					ui.Text(c, hint).Font(monoFont).FontSize(px(t, fsMicro)).TextColor(k.Text3).SingleLine()
				})
			}
			section(i18n.T("Pauses"))
			for _, it := range pauseItems() {
				item(it, false)
			}
			section(i18n.T("Delivery"))
			for _, it := range deliveryItems() {
				item(it, (it.key == "slow" || it.key == "fast") && !tr.Speed)
			}
			item(insertItem{key: "say", label: i18n.T("Pronounce as…"), open: "[[", close: "|]]", hint: "[[word|say]]"}, false)
			section(i18n.T("Expressions"))
			if !tr.NonVerbal {
				ui.Text(c, i18n.T("%s cannot voice expressions; they are left out.", a.preset().Label)).
					FontSize(px(t, fsMicro)).TextColor(k.Text3).Padding(0, px(t, sp2), px(t, 4))
			}
			ui.Scroll(c).MaxHeight(px(t, 220)).Children(func() {
				ui.Column(c).Children(func() {
					for _, tag := range markup.DefaultNonVerbal {
						item(insertItem{key: tag, label: nonVerbalLabel(tag), open: "[" + tag + "]", nonVerbal: true}, !tr.NonVerbal)
					}
				})
			})
		})
	})
}

// languageSupport lists the language codes the model speaks, and maps a
// code to the value its language parameter takes. all is true when every
// language is offered (a custom model).
func languageSupport(p catalog.Entry) (codes []string, value func(code string) string, all bool) {
	value = func(code string) string { return code }
	switch {
	case p.ID == "custom":
		return nil, value, true
	case len(p.Languages) == 0 && p.Family == "omnivoice":
		return lang.OmniVoiceCodes(), value, false
	}
	byCode := map[string]string{}
	for _, l := range p.Languages {
		codes = append(codes, l.Code)
		byCode[l.Code] = l.Value
	}
	value = func(code string) string {
		if v := byCode[code]; v != "" {
			return v
		}
		return code
	}
	return codes, value, false
}

// languageLabel shows the form's language: flag and name, or Auto.
func (a *app) languageLabel() string {
	if a.language == "" {
		return i18n.T("Auto-detect")
	}
	p := a.preset()
	code := a.language
	for _, l := range p.Languages {
		if l.Value == a.language {
			code = l.Code
		}
	}
	if l, ok := lang.Lookup(code); ok {
		name := l.NameIn(i18n.Locale())
		if f := l.Flag(); f != "" {
			name = f + " " + name
		}
		return name
	}
	return a.language
}

// languageChosen sets the language and remembers it among the recent ones.
func (a *app) languageChosen(code, value string) {
	a.language = value
	r := a.recentLangs[:]
	r = slices.DeleteFunc(slices.Clone(r), func(s string) bool { return s == code || s == "" })
	r = append([]string{code}, r...)
	for i := range a.recentLangs {
		a.recentLangs[i] = ""
		if i < len(r) {
			a.recentLangs[i] = r[i]
		}
	}
	a.composer.langOpen = false
}

// languagePicker is the language button and its searchable list.
func (a *app) languagePicker(c *ui.Context) {
	t := c.Theme()
	p := a.preset()
	if !p.Has(catalog.CapLanguage) && p.ID != "custom" {
		return
	}
	btn := dropButton(c.Key("language"), icons.Global, a.languageLabel()).Tooltip(i18n.T("Language of the script"))
	if btn.Clicked() {
		a.composer.langOpen = !a.composer.langOpen
	}
	ui.Popover(c, btn, &a.composer.langOpen, func() {
		codes, valueOf, all := languageSupport(p)
		var langs []lang.Language
		if q := strings.TrimSpace(a.composer.langQuery); q != "" {
			langs = lang.Search(q)
		} else {
			langs = lang.All()
		}
		opts := lang.Annotate(langs, codes)
		if all {
			for i := range opts {
				opts[i].Supported, opts[i].ModelCode = true, opts[i].Code
			}
		}
		// What the model speaks first.
		slices.SortStableFunc(opts, func(x, y lang.Option) int {
			switch {
			case x.Supported == y.Supported:
				return 0
			case x.Supported:
				return -1
			}
			return 1
		})
		ui.Column(c.Key("lang-picker")).Width(px(t, 320)).Gap(px(t, 6)).Padding(px(t, 6)).Children(func() {
			ui.TextInput(c.Key("lang-search"), &a.composer.langQuery).Placeholder(i18n.T("Search 650 languages")).Label(i18n.T("Search languages"))
			ui.Row(c).Gap(px(t, 6)).Wrap().Children(func() {
				if ui.Button(c.Key("lang-auto"), i18n.T("Auto-detect")).Clicked() {
					a.language = ""
					a.composer.langOpen = false
				}
				for _, code := range a.recentLangs {
					if code == "" {
						continue
					}
					l, ok := lang.Lookup(code)
					if !ok {
						continue
					}
					mc, supported := lang.Match(codes, code)
					if all {
						mc, supported = code, true
					}
					if !supported {
						continue
					}
					if ui.Button(c.Key("lang-recent-"+code), strings.TrimSpace(l.Flag()+" "+l.NameIn(i18n.Locale()))).Clicked() {
						a.languageChosen(code, valueOf(mc))
					}
				}
			})
			ui.List(c.Key("lang-list"), &a.composer.langList, len(opts)).Height(px(t, 300)).ItemKey(func(i int) any { return opts[i].Code }).Rows(func(r ui.ListRow) {
				o := opts[r.Index]
				row := ui.ButtonBase(c.Key("lang-"+o.Code)).Padding(px(t, 4), px(t, 8)).Radius(5).Disabled(!o.Supported).
					Label(o.NameIn(i18n.Locale()))
				if o.Supported && row.Hovered() {
					row.Background(t.SurfaceHover)
				}
				if o.Supported && row.Clicked() {
					a.languageChosen(o.Code, valueOf(o.ModelCode))
				}
				row.Children(func() {
					muted := t.TextMuted
					text := t.Text
					if !o.Supported {
						text = muted
					}
					ui.Text(c, o.Flag()).Width(px(t, 22))
					ui.Column(c).Grow(1).MinWidth(0).Children(func() {
						name := o.NameIn(i18n.Locale())
						if o.Native != "" && o.Native != name {
							name += " · " + o.Native
						}
						ui.Text(c, name).TextColor(text).SingleLine().Ellipsis("…")
						if !o.Supported {
							ui.Text(c, i18n.T("Not supported by %s", p.Label)).FontSize(px(t, 11)).TextColor(muted).SingleLine().Ellipsis("…")
						}
					})
					ui.Text(c, o.Code).FontSize(px(t, 11)).TextColor(muted)
				})
			})
		})
	})
}

// mbPerMinute is how much a minute of mono audio takes at bits and rate.
func mbPerMinute(bits, rate int) float64 {
	if rate == 0 {
		rate = 24000
	}
	return float64(bits/8*rate*60) / 1e6
}

// tierSteps are the performance tiers' steps, in the Segmented's order.
var tierSteps = []float64{services.StepsFast, services.StepsBalanced, services.StepsQuality}

// qualityPopover is the Audio quality button and its popover.
func (a *app) qualityPopover(c *ui.Context) {
	t := c.Theme()
	p := a.preset()
	tr := a.traits()
	btn := a.composerButton(c, "quality", icons.TuningSquare2, i18n.T("Audio quality")).Tooltip(i18n.T("Bit depth, refinement and volume"))
	if btn.Clicked() {
		a.composer.qualityOpen = !a.composer.qualityOpen
	}
	ui.Popover(c, btn, &a.composer.qualityOpen, func() {
		popoverWithFooter(c, "quality", func() {
			if secondaryIconButton(c.Key("quality-reset"), icons.Restart, i18n.T("Reset")).Label(i18n.T("Reset")).Clicked() {
				a.bitsIndex, a.steps, a.evenOut, a.normalize = 0, 0, true, true
			}
		}, func() {
			ui.Column(c.Key("quality-pop")).Width(px(t, 320)).Gap(px(t, 10)).Padding(px(t, 10)).Children(func() {
				ui.Text(c, i18n.T("WAV precision")).FontWeight(600)
				segmentedFit(c.Key("bits"), &a.bitsIndex, i18n.T("16-bit"), i18n.T("24-bit"), i18n.T("32-bit float")).Label(i18n.T("WAV precision"))
				desc := []string{i18n.T("Standard: plays everywhere."), i18n.T("For editing: more headroom."), i18n.T("Original: the model's own samples.")}
				bits := bitDepths[min(max(a.bitsIndex, 0), 2)]
				caption(c, desc[min(max(a.bitsIndex, 0), 2)]+" "+i18n.T("About %.1f MB a minute.", mbPerMinute(bits, p.SampleRate)))
				if tr.Steps {
					ui.Divider(c)
					steps := a.steps
					if steps == 0 {
						steps = services.StepsBalanced
					}
					ui.Text(c, i18n.T("Voice refinement  %d steps", int(steps))).FontWeight(600)
					tier := slices.Index(tierSteps, steps)
					if segmentedFit(c.Key("tier"), &tier, i18n.T("Fast"), i18n.T("Balanced"), i18n.T("Quality")).Label(i18n.T("Performance")).Changed() && tier >= 0 {
						a.steps = tierSteps[tier]
					}
					if ui.StepSlider(c.Key("steps"), &steps, 8, 64, 4).Label(i18n.T("Refinement steps")).Changed() {
						a.steps = steps
					}
					caption(c, i18n.T("More steps sound cleaner and take longer."))
				}
				ui.Divider(c)
				formRow(c, "even-out-row", i18n.T("Even out volume"), i18n.T("Masters the take to broadcast loudness (−18 LUFS)."), func() {
					ui.Switch(c.Key("even-out"), &a.evenOut).Label(i18n.T("Even out volume"))
				})
				formRow(c, "normalize-row", i18n.T("Read numbers as words"), i18n.T("Numbers, times, dates, money and abbreviations, before the model reads them."), func() {
					ui.Switch(c.Key("normalize"), &a.normalize).Label(i18n.T("Read numbers as words"))
				})
			})
		})
	})
}

// voicePopover is the Voice controls button and its popover.
func (a *app) voicePopover(c *ui.Context) {
	t := c.Theme()
	p := a.preset()
	btn := a.composerButton(c, "voice-controls", icons.Tuning2, i18n.T("Voice controls")).Tooltip(i18n.T("Speed, length, seed and sampling"))
	if btn.Clicked() {
		a.composer.voiceOpen = !a.composer.voiceOpen
	}
	ui.Popover(c, btn, &a.composer.voiceOpen, func() {
		popoverWithFooter(c, "voice", func() {
			if secondaryIconButton(c.Key("voice-reset"), icons.Restart, i18n.T("Reset")).Label(i18n.T("Reset")).Clicked() {
				a.speed, a.duration, a.seedPinned, a.modelDefaults = 1, 0, false, true
				a.guidance, a.exaggeration = 0, 0
				a.temperature, a.topP, a.maxTokens = 0.7, 0.9, 1200
			}
		}, func() {
			ui.Column(c.Key("voice-pop")).Width(px(t, 320)).Gap(px(t, 10)).Padding(px(t, 10)).Children(func() {
				if p.Has(catalog.CapSpeed) {
					ui.Field(c, i18n.T("Speed  %.2f×", a.speed), func() {
						snapped(ui.Slider(c.Key("speed"), &a.speed, 0.5, 2).Label(i18n.T("Speed")), &a.speed, 0.05)
					})
				}
				if p.Has(catalog.CapDuration) {
					ui.Field(c, i18n.T("Duration (seconds, 0 = natural)"), func() {
						numberInput(c.Key("duration"), &a.duration, 0, 600, 0.5, numOpts{Label: i18n.T("Duration")})
					})
					if a.duration > 0 && p.Has(catalog.CapSpeed) {
						caption(c, i18n.T("A duration overrides the speed."))
					}
				}
				formRow(c, "seed-pin-row", i18n.T("Pin the seed"), i18n.T("The same seed and settings make the same take."), func() {
					ui.Switch(c.Key("seed-pin"), &a.seedPinned).Label(i18n.T("Pin the seed"))
				})
				if a.seedPinned {
					ui.Row(c).Gap(px(t, 8)).AlignItems(ui.Center).Children(func() {
						numberInput(c.Key("seed"), &a.seed, 1, 1<<31-1, 1, numOpts{Label: i18n.T("Seed"), Grow: true})
						if ui.Button(c.Key("new-seed"), i18n.T("New seed")).Clicked() {
							a.newSeed()
						}
					})
				}
				if p.Has(catalog.CapGuidance) {
					g := a.guidance
					if g == 0 {
						g = 2
					}
					ui.Field(c, i18n.T("Prompt guidance  %.1f", g), func() {
						if snapped(ui.Slider(c.Key("guidance"), &g, 1, 4).Label(i18n.T("Prompt guidance")), &g, 0.1).Changed() {
							a.guidance = g
						}
					})
				}
				if p.Has(catalog.CapExaggeration) {
					x := a.exaggeration
					if x == 0 {
						x = 0.5
					}
					ui.Field(c, i18n.T("Expressiveness  %.2f", x), func() {
						if snapped(ui.Slider(c.Key("exaggeration"), &x, 0.25, 2).Label(i18n.T("Expressiveness")), &x, 0.05).Changed() {
							a.exaggeration = x
						}
					})
				}
				if p.Has(catalog.CapSampling) {
					ui.Divider(c)
					ui.Checkbox(c.Key("defaults"), &a.modelDefaults, i18n.T("Use the model's sampling defaults"))
					if !a.modelDefaults {
						ui.Field(c, i18n.T("Temperature  %.2f", a.temperature), func() {
							snapped(ui.Slider(c.Key("temperature"), &a.temperature, 0, 1.5).Label(i18n.T("Temperature")), &a.temperature, 0.05)
						})
						ui.Field(c, i18n.T("Top P  %.2f", a.topP), func() {
							snapped(ui.Slider(c.Key("topp"), &a.topP, 0.05, 1).Label(i18n.T("Top P")), &a.topP, 0.05)
						})
						ui.Field(c, i18n.T("Max tokens"), func() {
							numberInput(c.Key("maxtokens"), &a.maxTokens, 50, 8000, 50, numOpts{Label: i18n.T("Max tokens")})
						})
					}
				}
			})
		})
	})
}

// voiceControlsSummary says what the Voice controls change, for the bar.
func (a *app) voiceControlsSummary() string {
	p := a.preset()
	var parts []string
	if p.Has(catalog.CapDuration) && a.duration > 0 {
		parts = append(parts, i18n.T("%.1f s", a.duration))
	} else if p.Has(catalog.CapSpeed) && a.speed != 1 {
		parts = append(parts, fmt.Sprintf("%.2f×", a.speed))
	}
	if a.seedPinned && a.seed > 0 {
		parts = append(parts, i18n.T("seed %d", int64(a.seed)))
	}
	return strings.Join(parts, " · ")
}

// composerButton is a button of the composer that opens a popover: its
// icon and label, or its icon alone when the composer is narrow.
func (a *app) composerButton(c *ui.Context, key string, ic icons.Icon, label string) ui.Element {
	if a.studioNarrow(c) {
		return iconButton(c.Key(key), ic, label)
	}
	return secondaryIconButton(c.Key(key), ic, label).Label(label)
}
