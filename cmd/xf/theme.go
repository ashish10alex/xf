package main

import (
	"github.com/ashish10alex/xf/internal/config"
	"github.com/ashish10alex/xf/internal/webtheme"
)

// saveTheme saves the TUI's colour theme the user chose to their config
// file; the theme is the user's, so never the project's.
func saveTheme(theme string) error {
	_, err := config.SetUserKey(config.KeyTheme, theme)
	return err
}

// webTheme is the theme xf web starts with, with the themes its auto
// shows, and saves what the page picks: webTheme is the --web-theme flag,
// until the page changes it, else the config files', the project's at
// root winning over the user's. It is read afresh, so a change made
// elsewhere shows on a reload. set saves to the user's file, or with a
// root to that project's, where an empty id takes the key out.
func webTheme(sf *settingFlags) (get func(root string) webtheme.Choice, set func(root, key, id string) error) {
	flag := ""
	if v := sf.values[config.KeyWebTheme]; v != nil {
		flag = *v
	}
	get = func(root string) webtheme.Choice {
		var c webtheme.Choice
		if l, err := config.Load(config.Options{Root: root}); err == nil {
			c = webtheme.Choice{Theme: l.WebTheme, Light: l.WebThemeLight, Dark: l.WebThemeDark}
		}
		if flag != "" {
			c.Theme = flag
		}
		return c
	}
	set = func(root, key, id string) error {
		if key == config.KeyWebTheme {
			flag = ""
		}
		var err error
		switch {
		case root == "":
			_, err = config.SetUserKey(key, id)
		case id == "":
			_, err = config.UnsetProjectKey(root, key)
		default:
			_, err = config.SetProjectKey(root, key, id)
		}
		return err
	}
	return get, set
}

// pickerTheme is the theme the project picker shows: the user's, which no
// project can set.
func pickerTheme(sf *settingFlags) string {
	l, err := sf.load("")
	if err != nil {
		return ""
	}
	return l.Theme
}
