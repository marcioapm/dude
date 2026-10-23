package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// `dude login URL` keeps one profile per organisation, under its slug:
// the server's URL in the credentials file, and the key in the OS's own
// store (the macOS keychain, the Secret Service through secret-tool on
// Linux and the BSDs, the Windows Credential Manager) — or, on a machine
// with none, in the file beside the URL. The CLI acts in the organisation
// last logged in to, or in --org / DUDE_ORG.
//
//	# ~/.config/dude/credentials.toml, mode 0600
//	current = "acme"
//	[acme]
//	url = "https://dude.acme.dev"
//	[northwind]
//	url = "https://dude.acme.dev"
//	key = "dude_sk_…"            # only when there is no OS store
//
// DUDE_API_KEY and DUDE_URL override both, for scripts and CI.

var (
	errNoKey        = errors.New("no key stored")
	errNoKeystore   = errors.New("no keystore on this machine")
	credentialsFile = defaultCredentialsFile
)

// keystore is the OS's secret store, one secret per id; osKeystore is per
// OS (keystore_*.go).
type keystore interface {
	name() string
	get(id string) (string, error) // errNoKey, errNoKeystore
	set(id, key string) error
	del(id string) error
}

type profile struct {
	URL string `toml:"url"`
	Key string `toml:"key,omitempty"`
}

type credentials struct {
	Current string
	Orgs    map[string]profile
}

func hostOf(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", fmt.Errorf("%q is not a dude server's URL", base)
	}
	return u.Hostname(), nil
}

// normalURL is a server URL as profiles keep it: scheme and host, no slash.
func normalURL(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	if _, err := hostOf(raw); err != nil {
		return "", err
	}
	return strings.TrimRight(raw, "/"), nil
}

// keyID names an organisation's key in the OS store: HOST/SLUG, the host
// lower-cased since Windows matches credential names without case.
func keyID(slug, base string) string {
	u, _ := url.Parse(base)
	return strings.ToLower(u.Host) + "/" + slug
}

// sameServer is whether two server URLs name one server: scheme, host
// (without case) and port, the default port and a missing scheme as login reads them.
func sameServer(a, b string) bool {
	canon := func(raw string) string {
		n, err := normalURL(raw)
		if err != nil {
			return raw
		}
		u, _ := url.Parse(n)
		host := strings.ToLower(u.Hostname())
		if port := u.Port(); port != "" && !(u.Scheme == "https" && port == "443") && !(u.Scheme == "http" && port == "80") {
			host += ":" + port
		}
		return u.Scheme + "://" + host
	}
	return canon(a) == canon(b)
}

// defaultCredentialsFile is $XDG_CONFIG_HOME/dude/credentials.toml, or
// ~/.config/… — on macOS too, as accounts.md names it; %AppData% on Windows.
func defaultCredentialsFile() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" && runtime.GOOS == "windows" {
		dir, _ = os.UserConfigDir()
	}
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "dude", "credentials.toml")
}

func loadCredentials() (*credentials, error) {
	c := &credentials{Orgs: map[string]profile{}}
	raw, err := os.ReadFile(credentialsFile())
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", credentialsFile(), err)
	}
	for k, v := range doc {
		switch v := v.(type) {
		case string:
			if k == "current" {
				c.Current = v
			}
		case map[string]any:
			p := profile{}
			p.URL, _ = v["url"].(string)
			p.Key, _ = v["key"].(string)
			c.Orgs[k] = p
		}
	}
	return c, nil
}

func (c *credentials) save() error {
	path := credentialsFile()
	if len(c.Orgs) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	doc := map[string]any{}
	if c.Current != "" {
		doc["current"] = c.Current
	}
	for slug, p := range c.Orgs {
		doc[slug] = p
	}
	raw, err := toml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *credentials) slugs() []string {
	out := make([]string, 0, len(c.Orgs))
	for s := range c.Orgs {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// selected is the organisation a command acts in: --org, DUDE_ORG, or the
// last one logged in to; explicit says it was asked for by name.
func (c *credentials) selected(flagOrg string) (slug string, explicit bool) {
	if flagOrg != "" {
		return flagOrg, true
	}
	if o := os.Getenv("DUDE_ORG"); o != "" {
		return o, true
	}
	return c.Current, false
}

// resolve is the server and key a command uses.
func resolve(flagOrg string) (base, key string, err error) {
	envURL, envKey := os.Getenv("DUDE_URL"), os.Getenv("DUDE_API_KEY")
	creds, err := loadCredentials()
	if err != nil {
		return "", "", err
	}
	slug, explicit := creds.selected(flagOrg)
	p, ok := creds.Orgs[slug]
	if slug != "" && !ok && explicit {
		return "", "", fmt.Errorf("not logged in to %q: `dude orgs` lists the ones you are logged in to, `dude login URL` adds one", slug)
	}
	if ok {
		base = p.URL
		if envURL != "" {
			base = envURL
		}
		if envKey != "" {
			return strings.TrimRight(base, "/"), envKey, nil
		}
		if envURL != "" && !sameServer(envURL, p.URL) {
			return "", "", fmt.Errorf("DUDE_URL %q differs from %s's stored URL %q: set DUDE_API_KEY too, or unset DUDE_URL", envURL, slug, p.URL)
		}
		base = p.URL // DUDE_URL names the same server; the stored form is the one to use
		if p.Key != "" {
			return base, p.Key, nil
		}
		k, err := osKeystore.get(keyID(slug, p.URL))
		if err == nil {
			return base, k, nil
		}
		if errors.Is(err, errNoKey) || errors.Is(err, errNoKeystore) {
			return "", "", fmt.Errorf("the key for %s is gone from the %s: `dude login %s` again", slug, osKeystore.name(), p.URL)
		}
		return "", "", fmt.Errorf("reading the key for %s from the %s: %w", slug, osKeystore.name(), err)
	}
	// Not logged in: DUDE_URL or the default, with DUDE_API_KEY, or a key
	// stored under the bare host before profiles existed
	// (security add-generic-password -s HOST).
	base = strings.TrimRight(envURL, "/")
	if base == "" {
		base = defaultURL
	}
	if envKey != "" {
		return base, envKey, nil
	}
	host, err := hostOf(base)
	if err != nil {
		return "", "", err
	}
	if k, err := osKeystore.get(host); err == nil {
		return base, k, nil
	}
	return "", "", errors.New("not logged in: `dude login URL` (the key is made in dude: Your settings → Keys), or set DUDE_API_KEY")
}

// login keeps key as slug's at base and makes slug current. It says where
// the key went.
func login(slug, base, key string, toFile bool) (string, error) {
	creds, err := loadCredentials()
	if err != nil {
		return "", err
	}
	old, had := creds.Orgs[slug]
	p := profile{URL: base}
	where := ""
	if !toFile {
		switch err := osKeystore.set(keyID(slug, base), key); {
		case err == nil:
			where = osKeystore.name()
		case !errors.Is(err, errNoKeystore):
			return "", fmt.Errorf("storing the key in the %s: %w (--file keeps it in %s instead)",
				osKeystore.name(), err, credentialsFile())
		}
	}
	if where == "" {
		p.Key = key
		where = credentialsFile()
	}
	// Moving servers, or from the OS store to the file: the old key goes.
	if had && (keyID(slug, old.URL) != keyID(slug, base) || p.Key != "") {
		_ = osKeystore.del(keyID(slug, old.URL))
	}
	creds.Orgs[slug] = p
	creds.Current = slug
	if err := creds.save(); err != nil {
		return "", err
	}
	return where, nil
}

// logout forgets slug's key and profile. A current slug passes current to
// the first one left.
func logout(slug string) error {
	creds, err := loadCredentials()
	if err != nil {
		return err
	}
	p, ok := creds.Orgs[slug]
	if !ok {
		return fmt.Errorf("not logged in to %q", slug)
	}
	if err := osKeystore.del(keyID(slug, p.URL)); err != nil && !errors.Is(err, errNoKey) && !errors.Is(err, errNoKeystore) {
		return err
	}
	delete(creds.Orgs, slug)
	if creds.Current == slug {
		creds.Current = ""
		if rest := creds.slugs(); len(rest) > 0 {
			creds.Current = rest[0]
		}
	}
	return creds.save()
}
