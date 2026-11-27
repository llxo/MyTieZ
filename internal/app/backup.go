package app

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"mytiez/internal/store"
	"mytiez/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func (a *clipApp) startExportBackup() {
	if a.backupBusy || a.store == nil {
		return
	}
	a.backupDialogMode = "export"
	a.backupPassword = ""
	a.backupImportPath = ""
	a.backupDialogOpen = true
}

func (a *clipApp) startImportBackup() {
	if a.backupBusy || a.store == nil || a.pickingApp {
		return
	}
	panel := a.panel
	a.pickingApp = true
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: panel,
			Title:  locales.SettingsImportDialogTitle(),
			Filters: []mygo.FileFilter{
				{Name: locales.SettingsBackupFilterZip(), Extensions: []string{"zip"}},
			},
		})
		if panel == nil {
			return
		}
		panel.Update(func() {
			a.pickingApp = false
			if err != nil {
				a.setStatus(locales.SettingsImportErr(err.Error()))
				return
			}
			if len(paths) == 0 || strings.TrimSpace(paths[0]) == "" {
				return
			}
			zipPath := paths[0]
			manifest, inspectErr := store.InspectBackupZip(zipPath)
			if inspectErr != nil {
				a.setStatus(locales.SettingsImportErr(inspectErr.Error()))
				return
			}
			if manifest.Encrypted {
				a.backupDialogMode = "import"
				a.backupImportPath = zipPath
				a.backupPassword = ""
				a.backupDialogOpen = true
				return
			}
			a.runImportBackup(zipPath, "")
		})
	}()
}

func (a *clipApp) confirmBackupDialog() {
	if a.backupBusy {
		return
	}
	pw := a.backupPassword
	mode := a.backupDialogMode
	importPath := a.backupImportPath
	if mode == "export" {
		a.backupDialogOpen = false
		a.backupPassword = ""
		a.pickAndExportBackup(pw)
		return
	}
	if mode == "import" && importPath != "" {
		a.runImportBackup(importPath, pw)
	}
}

func (a *clipApp) pickAndExportBackup(password string) {
	if a.backupBusy || a.store == nil || a.pickingApp {
		return
	}
	panel := a.panel
	a.pickingApp = true
	defaultName := fmt.Sprintf("mytiez-backup-%s.zip", time.Now().Format("20060102-150405"))
	go func() {
		dstPath, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent:      panel,
			Title:       locales.SettingsExportDialogTitle(),
			DefaultPath: defaultName,
			Filters: []mygo.FileFilter{
				{Name: locales.SettingsBackupFilterZip(), Extensions: []string{"zip"}},
			},
		})
		if panel == nil {
			return
		}
		if err != nil {
			panel.Update(func() {
				a.pickingApp = false
				a.setStatus(locales.SettingsExportErr(err.Error()))
			})
			return
		}
		if strings.TrimSpace(dstPath) == "" {
			panel.Update(func() {
				a.pickingApp = false
			})
			return
		}
		panel.Update(func() {
			a.pickingApp = false
			a.backupBusy = true
		})

		res, exportErr := a.store.ExportBackupZip(dstPath, password)
		panel.Update(func() {
			a.backupBusy = false
			if exportErr != nil {
				a.setStatus(locales.SettingsExportErr(exportErr.Error()))
				return
			}
			a.setStatus(locales.SettingsExportDone(strconv.Itoa(res.ItemCount), strconv.Itoa(res.ImageCount)))
		})
	}()
}

func (a *clipApp) runImportBackup(zipPath, password string) {
	if a.backupBusy || a.store == nil {
		return
	}
	panel := a.panel
	a.backupBusy = true
	go func() {
		res, err := a.store.ImportBackupZip(zipPath, password)
		if panel == nil {
			return
		}
		panel.Update(func() {
			a.backupBusy = false
			if err != nil {
				if errors.Is(err, store.ErrBackupInvalidPassword) {
					a.backupPassword = ""
					a.setStatus(locales.SettingsBackupWrongPassword())
					return
				}
				a.backupDialogOpen = false
				a.backupPassword = ""
				a.setStatus(locales.SettingsImportErr(err.Error()))
				return
			}
			a.backupDialogOpen = false
			a.backupPassword = ""
			a.backupImportPath = ""
			a.reload()
			a.setStatus(locales.SettingsImportDone(strconv.Itoa(res.Imported), strconv.Itoa(res.Skipped)))
		})
	}()
}

func (a *clipApp) renderBackupDialog(c *ui.Context) {
	if !a.backupDialogOpen {
		return
	}
	msgs := locales.In(c.Locale())
	t := c.Theme()

	title := msgs.SettingsExportPasswordTitle()
	desc := msgs.SettingsExportPasswordDesc()
	confirmLabel := msgs.SettingsExportNext()
	if a.backupDialogMode == "import" {
		title = msgs.SettingsImportPasswordTitle()
		desc = msgs.SettingsImportPasswordDesc()
		confirmLabel = msgs.SettingsImportBtn()
	}

	ui.DialogBase(c, &a.backupDialogOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.RGBA(0, 0, 0, 0.4))
		if backdrop.OverlayShortcut(0, ui.KeyEscape) {
			a.backupDialogOpen = false
			a.backupPassword = ""
			return
		}
		room := max(float32(260), a.panelWidth()-48)
		panel.Width(min(float32(320), room)).
			Padding(18).Gap(10).Radius(radiusCard).
			Background(a.floatingSurface(c, t)).Border(1, t.Border).
			Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, shadowAlpha(t, 0.16, 0.4))).
			Children(func() {
				ui.Text(c, title).FontSize(15).FontWeight(600).TextColor(t.Text)
				ui.Text(c, desc).FontSize(12.5).TextColor(t.TextMuted).Wrap()

				ui.Row(c).FillWidth().Height(36).Padding(0, 10).Radius(radiusGroup).
					Background(t.Surface).Border(1, t.Border).AlignItems(ui.Center).
					Children(func() {
						inp := ui.TextInputBase(c, &a.backupPassword).Bind(&a.backupPassHandle).
							Grow(1).Password().
							Placeholder(msgs.SettingsBackupPasswordPlaceholder()).
							FontSize(13).TextColor(t.Text).
							AutoFocus()
						if inp.Focused() && a.settings.NoFocusMode {
							a.activatePanelForInput()
						}
						if inp.Submitted() {
							a.confirmBackupDialog()
						}
					})

				ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Margin(4, 0, 0, 0).Children(func() {
					cancelBtn := ui.ButtonBase(c).Height(pillHeight).Padding(0, 14).Radius(radiusGroup).Label(msgs.DialogCancel())
					cBg := t.Surface
					if cancelBtn.Hovered() {
						cBg = t.SurfaceHover
					}
					cancelBtn.Background(cBg).Border(1, t.Border.Alpha(0.7)).Transition(colorTransition).
						OnClick(func() {
							a.backupDialogOpen = false
							a.backupPassword = ""
						}).
						Children(func() {
							ui.Text(c, msgs.DialogCancel()).FontSize(13).FontWeight(500).TextColor(t.Text)
						})

					okBtn := ui.ButtonBase(c).Height(pillHeight).Padding(0, 14).Radius(radiusGroup).Label(confirmLabel)
					oBg := t.Accent
					if okBtn.Hovered() {
						oBg = t.AccentHover
					}
					okBtn.Background(oBg).Transition(colorTransition).
						OnClick(func() {
							a.confirmBackupDialog()
						}).
						Children(func() {
							ui.Text(c, confirmLabel).FontSize(13).FontWeight(500).TextColor(t.AccentText)
						})
				})
			})
	})
}
