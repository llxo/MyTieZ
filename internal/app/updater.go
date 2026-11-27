package app

import (
	"context"
	"time"

	"mytiez/locales"

	"github.com/egoist/mygo"
)

const (
	updateCheckTimeout   = 10 * time.Second
	updateInstallTimeout = 5 * time.Minute
	autoUpdateInterval   = 24 * time.Hour
)

func appVersion() string {
	if mygo.App.IsReady() {
		if v := mygo.App.Version(); v != "" {
			return v
		}
	}
	return "dev"
}

func (a *clipApp) checkForUpdates(manual bool) {
	if a.updateChecking || a.updateInstalling {
		return
	}
	if !manual && a.settings.LastUpdateCheck > 0 && time.Since(time.Unix(a.settings.LastUpdateCheck, 0)) < autoUpdateInterval {
		return
	}
	if !mygo.App.IsReady() || !mygo.Updater.Enabled() {
		if manual {
			a.setStatus(locales.SettingsUpdateDisabled())
		}
		return
	}
	a.updateChecking = true
	panel := a.panel
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
		defer cancel()
		up, err := mygo.Updater.Check(ctx)
		if panel == nil {
			return
		}
		panel.Update(func() {
			a.updateChecking = false
			if err != nil {
				if manual {
					a.setStatus(locales.SettingsUpdateErr(err.Error()))
				}
				return
			}
			a.settings.LastUpdateCheck = time.Now().Unix()
			a.persistSettings()
			if up == nil {
				a.availableUpdate = nil
				a.updateReady = false
				if manual {
					a.setStatus(locales.SettingsUpToDate())
				}
				return
			}
			a.availableUpdate = up
			a.updateReady = false
			if !manual {
				a.setStatus(locales.SettingsUpdateAvailable(up.Version))
			}
		})
	}()
}

func (a *clipApp) installAvailableUpdate() {
	if a.updateInstalling || a.availableUpdate == nil || !mygo.App.IsReady() {
		return
	}
	up := a.availableUpdate
	a.updateInstalling = true
	a.updateProgress = 0
	panel := a.panel

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), updateInstallTimeout)
		defer cancel()
		lastPct := -1
		err := up.Install(ctx, func(downloaded, total int64) {
			if total <= 0 || panel == nil {
				return
			}
			pct := int(downloaded * 100 / total)
			if pct > 100 {
				pct = 100
			}
			if pct == lastPct || (pct < 100 && pct-lastPct < 2) {
				return
			}
			lastPct = pct
			panel.Update(func() {
				a.updateProgress = pct
			})
		})
		if panel == nil {
			return
		}
		panel.Update(func() {
			a.updateInstalling = false
			if err != nil {
				a.setStatus(locales.SettingsUpdateErr(err.Error()))
				return
			}
			a.updateReady = true
		})
	}()
}
