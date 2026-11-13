package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"sharetoken/internal/node"
)

// What the command line and the local API both do to home's providers file.

// AddRequest describes a provider reached with an API key.
type AddRequest struct {
	Type    string            `json:"type"`
	Name    string            `json:"name,omitempty"` // default: the type
	APIKey  string            `json:"api_key,omitempty"`
	BaseURL string            `json:"base_url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// NoCheck saves the provider without asking it whether the key works.
	NoCheck bool `json:"no_check,omitempty"`
}

// Added is the answer to adding a provider or logging in to one.
type Added struct {
	Provider View         `json:"provider"`
	Check    *CheckResult `json:"check,omitempty"`
}

// AddWithKey checks an API-key provider and saves it, enabled. A provider of
// the same name and type keeps its models: this is how a key is replaced.
func AddWithKey(ctx context.Context, home string, req AddRequest) (*Added, error) {
	k, ok := LookupKind(req.Type)
	if !ok {
		return nil, fmt.Errorf("unknown provider type %q", req.Type)
	}
	e := &Entry{Name: req.Name, Type: k.ID, Auth: AuthAPIKey, Enabled: true, APIKey: strings.TrimSpace(req.APIKey),
		BaseURL: strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"), Headers: req.Headers, AddedAt: time.Now().UTC().Truncate(time.Second)}
	if e.Name == "" {
		e.Name = k.ID
	}
	e.Name = strings.ToLower(e.Name)
	if err := e.Validate(); err != nil {
		return nil, err
	}
	out := &Added{}
	if !req.NoCheck {
		r, err := e.Check(ctx, nil)
		if err != nil {
			return nil, err
		}
		out.Check = &r
	}
	return out, save(home, e, out)
}

// FinishLoginAndSave completes an OAuth login and saves the provider.
func FinishLoginAndSave(ctx context.Context, home, loginID, input, name string) (*Added, error) {
	tok, kind, err := FinishLogin(ctx, loginID, input)
	if err != nil {
		return nil, err
	}
	return SaveLogin(ctx, home, kind, name, tok)
}

// SaveLogin saves a provider with the token of a finished login.
func SaveLogin(ctx context.Context, home, kind, name string, tok *Token) (*Added, error) {
	if name == "" {
		name = kind
	}
	e := &Entry{Name: strings.ToLower(name), Type: kind, Auth: AuthOAuth, Enabled: true, OAuth: tok, AddedAt: time.Now().UTC().Truncate(time.Second)}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	out := &Added{}
	if ms, err := e.ListModels(ctx, FixedToken(tok)); err == nil {
		out.Check = &CheckResult{Models: ms}
	}
	return out, save(home, e, out)
}

func save(home string, e *Entry, out *Added) error {
	return Update(home, func(f *File) error {
		if old := f.Get(e.Name); old != nil && old.Type != e.Type {
			return fmt.Errorf("a %s provider is already called %q: give this one another name, or remove that one first", old.Type, e.Name)
		} else if old != nil {
			e.Enabled = old.Enabled
		}
		if err := f.Put(e); err != nil {
			return err
		}
		out.Provider = e.View()
		return nil
	})
}

// SetEnabled turns a provider on or off.
func SetEnabled(home, name string, on bool) (View, error) {
	var v View
	return v, Update(home, func(f *File) error {
		e, err := f.Find(name)
		if err != nil {
			return err
		}
		e.Enabled = on
		v = e.View()
		return nil
	})
}

// Remove deletes a provider with its credentials and models.
func Remove(home, name string) error {
	return Update(home, func(f *File) error {
		if !f.Remove(name) {
			return fmt.Errorf("%w: %q", ErrNotFound, name)
		}
		return nil
	})
}

// ErrModelTaken is returned for a network model ID that is already sold.
var ErrModelTaken = errors.New("model ID already in use")

// SetModel adds a model to what a provider sells, or replaces the one of the
// same ID. taken are the IDs in use outside the providers file.
func SetModel(home, name string, m node.ModelConfig, taken []string) (View, error) {
	var v View
	return v, Update(home, func(f *File) error {
		e, err := f.Find(name)
		if err != nil {
			return err
		}
		if slices.Contains(taken, m.ID) {
			return fmt.Errorf("%w: %s is a model of the local model server", ErrModelTaken, m.ID)
		}
		for _, o := range f.Providers {
			if o == e {
				continue
			}
			for _, om := range o.Models {
				if om.ID == m.ID {
					return fmt.Errorf("%w: provider %s already sells %s (choose another with -id)", ErrModelTaken, o.Name, m.ID)
				}
			}
		}
		e.Models = append(slices.DeleteFunc(e.Models, func(o node.ModelConfig) bool { return o.ID == m.ID }), m)
		if err := e.Validate(); err != nil {
			return err
		}
		v = e.View()
		return nil
	})
}

// RemoveModel stops selling a model. It reports which provider sold it, or
// "" when none did.
func RemoveModel(home, id string) (string, error) {
	var from string
	err := Update(home, func(f *File) error {
		for _, e := range f.Providers {
			n := len(e.Models)
			e.Models = slices.DeleteFunc(e.Models, func(m node.ModelConfig) bool { return m.ID == id })
			if len(e.Models) != n {
				from = e.Name
			}
		}
		return nil
	})
	return from, err
}

// With loads a provider and runs fn with it and its token source.
func With(home, name string, fn func(e *Entry, ts TokenSource) error) error {
	f, err := Load(home)
	if err != nil {
		return err
	}
	e, err := f.Find(name)
	if err != nil {
		return err
	}
	return fn(e, (&Tokens{Home: home}).For(e.Name))
}

// Models lists what a saved provider's credentials may use.
func Models(ctx context.Context, home, name string) ([]string, error) {
	var ms []string
	return ms, With(home, name, func(e *Entry, ts TokenSource) (err error) {
		ms, err = e.ListModels(ctx, ts)
		return err
	})
}

// Test sends one small request through a saved provider. model defaults to
// the first one it sells.
func Test(ctx context.Context, home, name, model, prompt string) (ProbeResult, string, error) {
	var r ProbeResult
	return r, model, With(home, name, func(e *Entry, ts TokenSource) error {
		if model == "" {
			if len(e.Models) == 0 {
				return errors.New("name a model to test with: this provider sells none yet")
			}
			model = e.Models[0].UpstreamModel
		}
		for _, m := range e.Models {
			if m.ID == model {
				model = m.UpstreamModel
			}
		}
		up, err := e.Upstream(ts)
		if err != nil {
			return err
		}
		r, err = Probe(ctx, up, model, prompt)
		return err
	})
}

// Views lists home's providers without their secrets.
func Views(home string) ([]View, error) {
	f, err := Load(home)
	if err != nil {
		return nil, err
	}
	out := []View{}
	for _, e := range f.Providers {
		out = append(out, e.View())
	}
	return out, nil
}

// KindView describes a kind for the list of what can be added.
type KindView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	APIKey   bool   `json:"apikey"`
	OAuth    bool   `json:"oauth"` // this build can do the login
	BaseURL  string `json:"base_url,omitempty"`
	KeyPage  string `json:"key_page,omitempty"`
	NeedsURL bool   `json:"needs_url,omitempty"`
	// KeyOptional: the API may need no key (a server on the local network).
	KeyOptional bool   `json:"key_optional,omitempty"`
	Note        string `json:"note,omitempty"`
}

// KindViews lists the kinds.
func KindViews() []KindView {
	var out []KindView
	for _, k := range kinds {
		out = append(out, KindView{ID: k.ID, Name: k.Name, APIKey: k.APIKey, OAuth: k.OAuth && OAuthSupported(k.ID),
			BaseURL: k.BaseURL, KeyPage: k.KeyPage, NeedsURL: k.BaseURL == "" && k.APIKey, KeyOptional: k.KeyOptional, Note: k.Note})
	}
	return out
}
