package i18n

import "testing"

func TestEnglishIsIdentity(t *testing.T) {
	SetLocale("en")
	if got := T("Generate"); got != "Generate" {
		t.Error(got)
	}
	if got := T("%d takes", 3); got != "3 takes" {
		t.Error(got)
	}
	if got := T("100%"); got != "100%" {
		t.Error("a message without args must not be formatted:", got)
	}
	if got := N(1, "%d take", "%d takes", 1); got != "1 take" {
		t.Error(got)
	}
	if got := N(2, "%d take", "%d takes", 2); got != "2 takes" {
		t.Error(got)
	}
	if got := X("verb", "Export"); got != "Export" {
		t.Error(got)
	}
}

func TestChinese(t *testing.T) {
	defer SetLocale("en")
	if got := SetLocale("zh_CN"); got != "zh-Hans" || Locale() != "zh-Hans" {
		t.Fatal(got)
	}
	if got := T("Generate"); got != "生成" {
		t.Error(got)
	}
	// Missing translations fall back to English.
	if got := T("No takes yet"); got != "No takes yet" {
		t.Error(got)
	}
	if got := N(1, "%d take", "%d takes", 1); got != "1 takes" {
		t.Error("zh-Hans has no singular, so it uses (the translation of) other:", got)
	}
}

func TestMatch(t *testing.T) {
	for in, want := range map[string]string{
		"": "en", "en": "en", "en-GB": "en", "fr-FR": "en",
		"zh": "zh-Hans", "zh-CN": "zh-Hans", "zh-Hans": "zh-Hans", "zh_Hans_CN": "zh-Hans", "zh-TW": "en", "ZH-HANS": "zh-Hans",
	} {
		if got := Match(in); got != want {
			t.Errorf("Match(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCatalogsParse(t *testing.T) {
	defer SetLocale("en")
	for _, tag := range Supported() {
		if got := SetLocale(tag); got != tag {
			t.Errorf("SetLocale(%s) = %s", tag, got)
		}
	}
}
