package normalize

import "testing"

func FuzzText(f *testing.F) {
	for _, s := range []string{"$5.50 at 3:30 on 2024-03-05", "第3章 50%", "1.000,5 €", "15h30", "3시 30분", "z. B. 5.3.2024"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, l := range Languages() {
			Text(s, l, Options{})
		}
	})
}
