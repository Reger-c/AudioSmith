package llm

import (
	"context"
	"testing"
)

// providerSecrets answers per provider, as the app's family keys do.
type providerSecrets struct{ asked []string }

func (p *providerSecrets) Secret(context.Context, string) (string, error) { return "", ErrNoSecret }
func (p *providerSecrets) ProviderSecret(_ context.Context, provider, name string) (string, error) {
	p.asked = append(p.asked, provider+" "+name)
	return "key-of-" + provider, nil
}

// Two providers keeping one Keychain item (siliconflow and its
// international site) are told apart by ProviderSecrets.
func TestProviderSecrets(t *testing.T) {
	ps := &providerSecrets{}
	for _, id := range []string{"siliconflow", "siliconflow-intl"} {
		p, err := ProviderConfig{ID: id}.Resolve()
		if err != nil {
			t.Fatal(err)
		}
		key, err := lookupKey(context.Background(), ps, p)
		if err != nil || key != "key-of-"+id {
			t.Errorf("%s: %q %v", id, key, err)
		}
	}
	if len(ps.asked) != 2 || ps.asked[0] != "siliconflow llm.siliconflow" || ps.asked[1] != "siliconflow-intl llm.siliconflow" {
		t.Errorf("asked %q", ps.asked)
	}
	// Plain Secrets still work.
	p, _ := ProviderConfig{ID: "deepseek"}.Resolve()
	if key, err := lookupKey(context.Background(), StaticSecrets{"llm.deepseek": " k "}, p); key != "k" || err != nil {
		t.Errorf("static: %q %v", key, err)
	}
}
