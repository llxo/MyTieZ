package app

import (
	"log"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"mytiez/internal/store"
	"mytiez/locales"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	searchRevealThreshold float32 = 16
	wheelDeltaMin         float32 = 6
	hideScrollOffset      float32 = 4
	atTopEpsilon          float32 = 12
	topSettleDuration             = 72 * time.Millisecond
)

func (a *clipApp) renderHeaderIconBtn(c *ui.Context, icon *ui.SVG, label string, active, danger bool, onClick func()) {
	t := c.Theme()
	btn := ui.ButtonBase(c).Size(headerBtnSize, headerBtnSize).Center().Radius(headerBtnRadius).Label(label).Tooltip(label)
	bg := t.Surface
	border := t.Border
	fg := t.Text
	if active {
		bg = t.Accent
		border = t.Accent
		fg = t.AccentText
		if btn.Hovered() {
			bg = t.AccentHover
			border = t.AccentHover
		}
	} else if btn.Hovered() {
		if danger {
			bg = t.Surface.Mix(t.Danger, 0.14)
			border = t.Danger
			fg = t.Danger
		} else {
			bg = t.SurfaceHover
			border = t.Border.Mix(t.TextMuted, 0.35)
			fg = t.Text
		}
	}
	if btn.Pressed() {
		btn.AnimatePose(ui.Pose{Scale: 0.92}, ui.Snappy)
	} else {
		btn.AnimatePose(ui.Pose{}, ui.Snappy)
	}
	sa := shadowAlpha(t, 0.06, 0.22)
	btn.Background(bg).Border(1, border).
		Shadow(0, 1, 3, 0, ui.RGBA(0, 0, 0, sa)).
		Transition(colorTransition).
		OnClick(onClick).
		Children(func() { ui.Icon(c, icon).FontSize(16).TextColor(fg) })
}

func (a *clipApp) renderWindowActionButtons(c *ui.Context) {
	msgs := locales.In(c.Locale())
	a.renderHeaderIconBtn(c, iconPin, msgs.HeaderPin(), a.winPinned, false, func() {
		a.toggleWindowPin()
	})
	a.renderHeaderIconBtn(c, iconClear, msgs.HeaderHide(), false, true, func() {
		a.hidePanel()
	})
}

func (a *clipApp) view(c *ui.Context) {
	defer func() { a.closeCardContextMenu = false }()
	if a.snapNextFrame {
		c.DisableAnimations()
		a.snapNextFrame = false
	}
	c.SetTheme(a.resolveTheme(c))
	t := c.Theme()
	c.Root().Background(a.rootBackground(c, t))

	ui.Box(c.Key("app-pages")).Fill().Clip().Children(func() {
		if a.showSettings {
			if !a.recordingHotkey && c.Shortcut(0, ui.KeyEscape) {
				if a.confirmAction != "" {
					a.confirmAction = ""
				} else {
					a.closeSettings()
				}
			}
			if a.showSettings {
				a.settingsView(c)
				return
			}
		}
		a.mainView(c)
	})
}

func (a *clipApp) mainView(c *ui.Context) {
	t := c.Theme()
	var enterPressed, plainPastePressed bool
	if !a.confirmClearOpen && !a.hasOpenFileMenu() {
		if a.editingTagsID != 0 && c.Shortcut(0, ui.KeyEscape) {
			a.editingTagsID = 0
			a.tagInput = ""
		} else if a.query != "" && c.Shortcut(0, ui.KeyEscape) {
			a.clearSearch(c)
		} else if a.selected >= 0 && c.Shortcut(0, ui.KeyEscape) {
			a.selected = -1
		} else if c.Shortcut(0, ui.KeyEscape) && a.panel != nil {
			a.hidePanel()
		}
		if c.Shortcut(0, ui.KeyDown) {
			a.moveSelection(1)
		}
		if c.Shortcut(0, ui.KeyUp) {
			a.moveSelection(-1)
		}
		a.handleQuickPasteShortcut(c)
		if a.editingTagsID == 0 {
			if mods, key, _, ok := parsePanelShortcut(a.settings.PlainPasteShortcut); ok && c.Shortcut(mods, key) {
				plainPastePressed = true
			}
		}
		if !plainPastePressed {
			enterPressed = c.Shortcut(0, ui.KeyEnter)
		}
	}

	msgs := locales.In(c.Locale())
	page := ui.Column(c.Key("main-page")).Fill()
	a.updateControlsCollapse(c)
	targetCtrl := float32(1)
	if a.controlsCollapsed {
		targetCtrl = 0
	}
	p := page.Animate("controls-open", targetCtrl, 180*time.Millisecond)
	page.Children(func() {
		// 顶部标题栏（常驻固定；搜索栏展开时收紧下边距，收起时保持 10px 平衡高度）
		ui.Column(c).Padding(8, 16, 10-5*p, 16).Children(func() {
			ui.Row(c).Height(headerBtnSize).AlignItems(ui.Center).Gap(5).DragWindow().Children(func() {
				ui.Text(c, "MyTieZ").FontSize(18).Bold().TextColor(t.Text)
				ui.Box(c).Grow(1)

				a.renderHeaderIconBtn(c, iconPin, msgs.HeaderPin(), a.winPinned, false, func() {
					a.toggleWindowPin()
				})
				a.renderHeaderIconBtn(c, iconTrash, msgs.HeaderClearUnpinned(), false, false, func() {
					a.closeFileMenus()
					a.confirmClearOpen = true
					a.stopKeyRouting()
				})
				a.renderHeaderIconBtn(c, iconSettings, msgs.HeaderSettings(), false, false, func() {
					a.closeFileMenus()
					a.showSettings = true
					a.confirmAction = ""
					a.stopKeyRouting()
				})
				a.renderHeaderIconBtn(c, iconClear, msgs.HeaderHide(), false, true, func() {
					a.hidePanel()
				})
			})
		})

		// 搜索框与分类筛选栏（离开顶部时收起，滚回最顶部再向上滚展开）
		ctrlWrap := ui.Column(c.Key("header-controls")).FillWidth()
		if p < 1 {
			const controlsFullHeight float32 = 92 // 2(top pad) + 46(search) + 8(gap) + 28(tabs) + 8(bottom pad)
			ctrlWrap.ClipY().Height(p * controlsFullHeight).Opacity(p)
		}
		if p > 0 {
			ctrlWrap.Padding(2, 16, 8, 16).Gap(8).Children(func() {
				searchBorder := t.Border
				if (!a.panelNoActivate && a.searchHandle.Focused(c)) || (a.panelNoActivate && a.routedSearchActive) {
					searchBorder = t.Accent
				}
				ui.Row(c).Height(searchBoxHeight).Padding(0, 12).Gap(8).AlignItems(ui.Center).Radius(radiusControl).
					Background(t.Surface).Border(1, searchBorder).Transition(colorTransition).
					ObserveInput(func(ev ui.InputEvent) {
						if ev.Kind == ui.InputPointerDown {
							a.onSearchBoxPointerDown()
						}
					}).
					Children(func() {
						ui.Icon(c, iconSearch).FontSize(18).TextColor(t.TextMuted)
						searchIn := ui.TextInputBase(c, &a.query).Bind(&a.searchHandle).
							Grow(1).FontSize(14).FontWeight(500).
							Placeholder(msgs.SearchPlaceholder()).Label(msgs.SearchLabel())
						if a.routedCaretPending {
							a.routedCaretPending = false
							n := utf8.RuneCountInString(a.query)
							searchIn.SetTextSelection(n, n)
						}
						if searchIn.Changed() && !searchIn.Composing() {
							a.scheduleSearchReload()
						}
						if a.query != "" {
							ui.ButtonBase(c).Size(24, 24).Center().Radius(radiusMicro).Label(msgs.SearchClear()).OnClick(func() {
								a.clearSearch(c)
							}).Children(func() {
								ui.Icon(c, iconClear).FontSize(16).TextColor(t.TextMuted)
							})
						}
					})

				ui.ScrollHorizontal(c).FillWidth().Scrollbars(false).Children(func() {
					tabLabels := []string{
						msgs.TabText(),
						msgs.TabImage(),
						msgs.TabFile(),
						msgs.TabURL(),
						msgs.TabCode(),
						msgs.TabRichText(),
					}
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						for i, label := range tabLabels {
							tabIdx := i + 1
							active := a.filterIdx == tabIdx
							tab := ui.ButtonBase(c).Height(filterPillHeight).Padding(0, 9).Radius(radiusMicro).Label(label)
							tabBg, tabBorder, tabText := t.Surface, t.Border, t.TextMuted
							tabWeight := 400
							if active {
								tabBg = t.Accent
								tabBorder = t.Accent
								tabText = t.AccentText
								tabWeight = 500
							} else if tab.Hovered() {
								tabBg = t.SurfaceHover
								tabBorder = t.Border.Mix(t.TextMuted, 0.35)
								tabText = t.Text
							}
							tab.Background(tabBg).Border(1, tabBorder).Transition(colorTransition).
								OnClick(func() {
									if a.filterIdx == tabIdx {
										a.filterIdx = 0
									} else {
										a.filterIdx = tabIdx
									}
									a.onTabChanged()
								}).
								Children(func() {
									ui.Text(c, label).FontSize(12).FontWeight(tabWeight).TextColor(tabText).SingleLine()
								})
						}
					})
				})
			})
		}

		ui.Divider(c)

		// 中部虚拟列表（直接铺满到底，无常驻底栏）
		a.consumePendingListReset()
		// Snapshot a.items for this frame so in-row actions (e.g. untagging under a #tag filter or deleting)
		// that call reload() mid-pass cannot shrink the slice before ui.List finishes iterating.
		items := a.items
		pinnedCount := 0
		for pinnedCount < len(items) && items[pinnedCount].Pinned {
			pinnedCount++
		}
		a.list.Selected = nil
		a.list.KeepOffset = true
		a.list.MoveAnimation = &ui.Snappy
		a.list.Reorder = nil
		a.list.Key = func(i int) any { return items[i].ID }
		a.list.Label = func(i int) string {
			it := items[i]
			if store.HasSensitiveTag(it.Tags) && !a.revealedSensitive[it.ID] {
				return msgs.CardSensitiveHidden()
			}
			return it.Preview
		}

		ui.Column(c).Grow(1).HandleInput(a.handleListWheel).Children(func() {
			firstVisible, lastVisible := len(items), -1
			list := ui.List(c, &a.list, len(items)).Bind(&a.listHandle).TrackScroll(&a.listScroll).
				Grow(1).Padding(10, 16, 12, 16).Gap(4).
				Rows(func(row ui.ListRow) {
					if row.Index < firstVisible {
						firstVisible = row.Index
					}
					if row.Index > lastVisible {
						lastVisible = row.Index
					}
					a.renderCard(row, items[row.Index], items, pinnedCount)
				}).
				Children(func() {
					if len(items) == 0 {
						ui.Column(c).Fill().Center().Padding(32).Gap(6).Children(func() {
							ui.Text(c, msgs.EmptyTitle()).FontSize(15).TextColor(t.TextMuted)
							ui.Text(c, msgs.EmptySubtitle()).FontSize(13).TextColor(t.TextMuted)
						})
					}
				})
			a.syncThumbWindow(items, firstVisible, lastVisible)

			if plainPastePressed {
				a.pasteSelectedPlainText()
			} else if enterPressed || list.Submitted() {
				a.pasteSelected()
			}
			a.renderScrollTopButton(c, t)
		})

		a.flushPendingToast(c)

		a.renderSearchTagPopover(c, t)
	})

	switch ui.AlertDialog(c, &a.confirmClearOpen, msgs.ConfirmClearUnpinnedTitle(), msgs.ConfirmClearUnpinnedMsg(), msgs.DialogCancel(), msgs.SettingsClearBtn()) {
	case 1:
		a.clearUnpinned()
	}
}

func (a *clipApp) flushPendingToast(c *ui.Context) {
	if a.pendingToast != "" {
		c.Toast(a.pendingToast)
		a.pendingToast = ""
	}
	t := c.Theme()
	sa := shadowAlpha(t, 0.12, 0.32)
	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Top(52).Justify(ui.Start).AlignItems(ui.Center).Gap(6)
		maxW := max(240, a.panelWidth()-48)
		for _, ts := range toasts {
			p := ui.ToastBase(c, ts)
			op := p.Root.Animate("toast-opacity", 1, 150*time.Millisecond)
			p.Root.Row().Justify(ui.Center).AlignItems(ui.Center).Gap(8).
				MinWidth(140).MaxWidth(maxW).
				Padding(11, 20).Radius(radiusCard).
				Background(a.floatingSurface(c, t)).Border(1, t.Border).
				Shadow(0, 6, 18, 0, ui.RGBA(0, 0, 0, sa)).
				Opacity(op).
				Children(func() {
					ui.Text(c, ts.Title).FontSize(13.5).FontWeight(500).TextColor(t.Text).Wrap()
				})
		}
	})
}

func (a *clipApp) reorderPinnedRows(items []store.ClipItem, pinnedCount int, rows []int, to int) {
	if a.store == nil || len(rows) != 1 || pinnedCount < 2 {
		return
	}
	from := rows[0]
	if from < 0 || from >= pinnedCount {
		// Ignore drags initiated from unpinned items.
		return
	}
	if to < 0 {
		to = 0
	} else if to > pinnedCount {
		to = pinnedCount
	}
	if to == from || to == from+1 {
		return
	}

	pinnedIDs := make([]int64, pinnedCount)
	for i := 0; i < pinnedCount; i++ {
		pinnedIDs[i] = items[i].ID
	}
	moved := pinnedIDs[from]
	pinnedIDs = append(pinnedIDs[:from], pinnedIDs[from+1:]...)
	if to > from {
		to--
	}
	pinnedIDs = append(pinnedIDs, 0)
	copy(pinnedIDs[to+1:], pinnedIDs[to:])
	pinnedIDs[to] = moved

	if err := a.store.ReorderPinned(pinnedIDs); err != nil {
		log.Printf("reorder pinned: %v", err)
		return
	}
	a.reload()
}

type pinnedCardDrag struct {
	index int
}

func (a *clipApp) renderCard(row ui.ListRow, item store.ClipItem, items []store.ClipItem, pinnedCount int) {
	c := row.Context
	t := c.Theme()
	msgs := locales.In(c.Locale())

	hovered := row.Hovered()
	pressed := row.Pressed()
	selected := a.selected >= 0 && a.selected == row.Index
	bg := t.Surface
	borderColor := t.Border
	var translateY float32
	shadowAlpha := float32(0)
	switch {
	case pressed:
		bg = t.SurfacePressed
		borderColor = t.Border.Mix(t.TextMuted, 0.35)
		shadowAlpha = 0.04
		if t.Dark {
			shadowAlpha = 0.16
		}
	case selected:
		bg = t.Surface.Mix(t.Accent, 0.08)
		borderColor = t.Accent
		shadowAlpha = 0.08
		if t.Dark {
			shadowAlpha = 0.28
		}
	case hovered:
		borderColor = t.Border.Mix(t.TextMuted, 0.28)
		translateY = -2
		shadowAlpha = 0.08
		if t.Dark {
			shadowAlpha = 0.28
		}
	}

	var padTop, padX, padBottom, gap float32 = 10, 12, 11, 0
	maxLines := 4
	bodySize := float32(14)
	if a.settings.CompactMode {
		padTop, padX, padBottom, gap = 6, 10, 6, 2
		maxLines = 1
		bodySize = 13
	}

	canDragPinned := item.Pinned && pinnedCount >= 2
	card := ui.Column(c).Padding(padTop, padX, padBottom, padX).Gap(gap).Radius(radiusCard).
		Background(bg).Border(1, borderColor).
		Shadow(0, 4, 12, 0, ui.RGBA(0, 0, 0, shadowAlpha)).
		AnimatePose(ui.Pose{Y: translateY}, ui.Animation{Duration: 240 * time.Millisecond, Ease: ui.EaseOut}).
		Transition(cardTransition)
	if canDragPinned {
		card.Drag(pinnedCardDrag{index: row.Index})
		if card.Dragging() {
			card.Opacity(0.4)
		}
		if over, ok := ui.DragOver[pinnedCardDrag](card); ok && over.index != row.Index {
			card.Border(1.5, t.Accent)
		}
	}
	card.
		Children(func() {
			ui.Row(c).MinHeight(cardActionSize).AlignItems(ui.Center).Gap(4).Children(func() {
				kindIcon, _ := kindMeta(msgs, item.Kind)
				if a.settings.ShowSourceIcon {
					if appBmp := a.getOrLoadAppIcon(item.SourcePath); appBmp != nil {
						ui.Image(c, appBmp).Size(14, 14).Fit(ui.Contain).Radius(3)
					} else {
						ui.Icon(c, kindIcon).FontSize(13).TextColor(t.TextMuted)
					}
				} else {
					ui.Icon(c, kindIcon).FontSize(13).TextColor(t.TextMuted)
				}
				sourceName := item.SourceApp
				if sourceName == "" {
					sourceName = "Unknown"
				}
				ui.Text(c, sourceName).FontSize(11.5).FontWeight(700).TextColor(t.TextMuted).SingleLine()

				ui.Box(c).Grow(1)

				isSensitive := store.HasSensitiveTag(item.Tags)
				isRevealed := isSensitive && a.revealedSensitive[item.ID]
				showAllActions := a.settings.ShowAllCardActions
				isMultiFile := showAllActions && item.Kind == store.KindFile && len(nonEmptyPaths(item.Files)) > 1
				fileMenuOpen := isMultiFile && a.isFileMenuOpen(item.ID)
				if isSensitive && (!a.settings.HideCardActions || hovered || isRevealed) {
					eyeIcon := iconEye
					eyeLabel := msgs.CardSensitiveShow()
					if isRevealed {
						eyeIcon = iconEyeOff
						eyeLabel = msgs.CardSensitiveHide()
					}
					a.renderCardActionBtn(c, eyeIcon, eyeLabel, isRevealed, func() {
						a.toggleSensitiveReveal(item.ID)
					})
				}

				if !a.settings.HideCardActions || hovered || fileMenuOpen {
					if showAllActions {
						if item.Kind == store.KindRichText {
							plainLabel := msgs.RichMenuPastePlain()
							if a.settings.ClickCopyOnly {
								plainLabel = msgs.RichMenuCopyPlain()
							}
							a.renderCardActionBtn(c, iconPlainPaste, plainLabel, false, func() {
								a.pasteItem(item, true)
							})
						}
						if isMultiFile {
							a.renderFileOpenMenuBtn(c, item, fileMenuOpen)
						} else {
							a.renderCardActionBtn(c, iconOpen, msgs.CardActionOpen(), false, func() {
								a.openItem(item)
							})
						}
						a.renderCardActionBtn(c, iconTag, msgs.CardActionTag(), len(item.Tags) > 0 || a.editingTagsID == item.ID, func() {
							a.toggleTagEditor(item.ID)
						})
					}
					a.renderCardActionBtn(c, iconPin, msgs.CardActionPin(), item.Pinned, func() {
						a.togglePin(item.ID)
					})
					a.renderCardActionBtn(c, iconClear, msgs.CardActionDelete(), false, func() {
						a.deleteItem(item.ID)
					})
				}

				ui.Text(c, relativeTime(msgs, item.UpdatedAt)).Width(48).TextAlign(ui.End).
					FontSize(11).FontWeight(400).TextColor(t.TextMuted).SingleLine()
			})

			if !a.settings.CompactMode {
				ui.Box(c).FillWidth().Height(1).Background(t.Text.Alpha(0.045))
				ui.Box(c).Height(3)
			}

			isHiddenSensitive := store.HasSensitiveTag(item.Tags) && !a.revealedSensitive[item.ID]
			if isHiddenSensitive {
				if item.Kind == store.KindImage {
					a.renderSensitiveImageMask(c, t, msgs)
				} else {
					masked := store.MaskSensitiveText(item.Preview, a.settings.SensitiveMaskPrefix, a.settings.SensitiveMaskSuffix, a.settings.SensitiveMaskEmailDomain)
					a.renderCardTextPreview(c, t, masked, []ui.Span{{Text: masked, Color: t.TextMuted}}, bodySize, maxLines, false)
				}
			} else {
				switch item.Kind {
				case store.KindText, store.KindCode:
					a.renderCardTextPreview(c, t, item.Preview, nil, bodySize, maxLines, false)
				case store.KindURL:
					a.renderCardTextPreview(c, t, item.Preview, []ui.Span{{Text: item.Preview, Color: t.Accent}}, bodySize, maxLines, false)
				case store.KindRichText:
					if spans := a.getRichPreviewSpans(item, t); len(spans) > 0 {
						a.renderCardTextPreview(c, t, item.Preview, spans, bodySize, maxLines, true)
					} else {
						a.renderCardTextPreview(c, t, item.Preview, nil, bodySize, maxLines, false)
					}
				case store.KindImage:
					a.renderImagePreview(c, item)
				case store.KindFile:
					a.renderFilesPreview(c, item)
				}
			}

			a.renderCardTags(c, t, item)
		})

	a.attachCardContextMenu(c, card, item)

	clicked := false
	if canDragPinned {
		if dragged, ok := ui.Drop[pinnedCardDrag](card); ok && dragged.index != row.Index {
			to := row.Index
			if dragged.index < row.Index {
				to = row.Index + 1
			}
			a.reorderPinnedRows(items, pinnedCount, []int{dragged.index}, to)
		}
		clicked = card.Clicked()
	} else {
		clicked = card.RowClicked()
	}
	if clicked {
		a.selected = row.Index
		a.pasteSelected()
		a.selected = -1
	}
}

func (a *clipApp) renderCardTextPreview(c *ui.Context, t *ui.Theme, text string, spans []ui.Span, bodySize float32, maxLines int, rich bool) {
	const lineHeight = 1.45
	if a.settings.CompactMode {
		if len(spans) > 0 {
			compactSpans := make([]ui.Span, len(spans))
			for i, sp := range spans {
				sp.Text = strings.ReplaceAll(sp.Text, "\n", " ")
				compactSpans[i] = sp
			}
			ui.RichText(c, compactSpans...).FontSize(bodySize).LineHeight(lineHeight).TextColor(t.Text).Wrap().MaxLines(maxLines)
		} else {
			ui.Text(c, strings.ReplaceAll(text, "\n", " ")).FontSize(bodySize).LineHeight(lineHeight).FontWeight(400).TextColor(t.Text).Wrap().MaxLines(maxLines)
		}
		return
	}

	// 普通卡片约 3.55 行高度；富文本卡片适度放宽至 4.55 行（约 92px，仍小于图片卡片 100px 上限）以保持整体视觉和谐
	previewLines := float32(3.55)
	displayMaxLines := 5
	fadeStart := float32(0.68)
	if rich && len(spans) > 0 {
		previewLines = 4.55
		displayMaxLines = 6
		fadeStart = 0.74
	}
	maxPreviewH := bodySize * lineHeight * previewLines
	contentW := a.cardContentWidth()

	var measureSpans []ui.Span
	if len(spans) > 0 {
		measureSpans = make([]ui.Span, len(spans))
		for i, sp := range spans {
			if sp.Size == 0 {
				sp.Size = bodySize
			}
			if sp.Font == "" {
				sp.Font = t.Font
			}
			measureSpans[i] = sp
		}
	} else {
		measureSpans = []ui.Span{{Text: text, Font: t.Font, Size: bodySize}}
	}
	_, measuredH := c.MeasureText(contentW, measureSpans...)
	// MeasureText uses default line height (~1.2x) because ui.Span has no LineHeight,
	// while rendering applies LineHeight(1.45); scale the threshold accordingly so
	// clipped previews still receive the bottom fade mask.
	overflows := measuredH > bodySize*previewLines*1.12

	box := ui.Box(c).FillWidth().MaxHeight(maxPreviewH).Clip()
	if overflows {
		black := ui.RGB(0, 0, 0)
		box.MaskLinearGradient(ui.LinearGradient{
			Angle: 180,
			Stops: []ui.GradientStop{
				{Color: black, At: 0},
				{Color: black, At: fadeStart},
				{Color: ui.Transparent, At: 1},
			},
		})
	}
	box.Children(func() {
		if len(spans) > 0 {
			ui.RichText(c, spans...).FontSize(bodySize).LineHeight(lineHeight).TextColor(t.Text).Wrap().MaxLines(displayMaxLines)
		} else {
			ui.Text(c, text).FontSize(bodySize).LineHeight(lineHeight).FontWeight(400).TextColor(t.Text).Wrap().MaxLines(displayMaxLines)
		}
	})
}

func (a *clipApp) clearSearch(c *ui.Context) {
	a.stopSearchTimer()
	a.query = ""
	a.selected = -1
	a.resetScrollToTop()
	a.reload()
	a.scrollListToTop()
	if c != nil {
		a.listHandle.Focus(c)
	}
}

func (a *clipApp) canAutoCollapseControls() bool {
	return !a.settings.PinSearchBox && len(a.items) > 0 && strings.TrimSpace(a.query) == ""
}

func (a *clipApp) handleQuickPasteShortcut(c *ui.Context) {
	var mod ui.Modifiers
	switch a.settings.QuickPasteModifier {
	case "alt":
		mod = ui.Alt
	case "ctrl":
		mod = ui.Ctrl
	default:
		return
	}
	for i := 0; i < 9; i++ {
		if c.Shortcut(mod, ui.Key(int(ui.Key1)+i)) {
			if i < len(a.items) {
				a.selected = i
				a.pasteSelected()
				a.selected = -1
			}
			return
		}
	}
}

func (a *clipApp) renderScrollTopButton(c *ui.Context, t *ui.Theme) {
	if !a.settings.ScrollTopButton || a.listScroll.Y <= 120 {
		return
	}
	sa := shadowAlpha(t, 0.12, 0.32)
	scrollTopLabel := locales.In(c.Locale()).ScrollTop()
	btn := ui.ButtonBase(c.Key("scroll-top-btn")).
		Absolute().Right(16).Bottom(16).ZIndex(20).
		Size(34, 34).Center().Radius(17).Label(scrollTopLabel).Tooltip(scrollTopLabel)
	bg := a.floatingSurface(c, t)
	fg := t.Text
	if btn.Hovered() {
		bg = t.Accent
		fg = t.AccentText
	}
	btn.Background(bg).Border(1, t.Border).
		Shadow(0, 4, 12, 0, ui.RGBA(0, 0, 0, sa)).
		Transition(colorTransition).
		OnClick(func() {
			a.listScroll.Y = 0
			a.atTop = true
			a.controlsCollapsed = false
			a.upScrollAccum = 0
			if len(a.items) > 0 {
				a.list.ScrollTo(0, ui.Start)
			}
		}).
		Children(func() {
			ui.Icon(c, iconArrowUp).FontSize(16).TextColor(fg)
		})
}

func (a *clipApp) updateControlsCollapse(c *ui.Context) {
	if !a.canAutoCollapseControls() {
		a.controlsCollapsed = false
		a.atTop = true
		a.upScrollAccum = 0
		return
	}
	offset := a.listScroll.Y
	nowAtTop := offset <= atTopEpsilon
	if !a.atTop && nowAtTop {
		// 刚滑回顶部：记下到达时间进入短暂冷却，避免惯性余波立刻弹出搜索栏
		a.topReachedAt = c.Now()
		a.upScrollAccum = 0
	}
	a.atTop = nowAtTop
	if offset > hideScrollOffset {
		a.controlsCollapsed = true
		a.upScrollAccum = 0
	}
}

func (a *clipApp) handleListWheel(ev ui.InputEvent) bool {
	if ev.Kind != ui.InputScroll || !a.canAutoCollapseControls() || ev.DY == 0 {
		return false
	}

	offset := a.listScroll.Y
	atTop := offset <= atTopEpsilon
	scrollingDown := ev.DY > 0
	scrollingUp := ev.DY < 0

	// 向下滚动且搜索栏当前展开：立即收起；若还在顶部，拦截本次滚动以免列表同时下跳
	if scrollingDown && !a.controlsCollapsed {
		if !atTop || a.listScroll.MaxY > 0 {
			a.controlsCollapsed = true
			a.upScrollAccum = 0
			if atTop {
				return true
			}
		}
		return false
	}

	absDY := ev.DY
	if absDY < 0 {
		absDY = -absDY
	}
	if absDY <= wheelDeltaMin || ev.Momentum {
		return false
	}

	// 已经在最顶部且冷却期过后，再主动向上滚一下才展开搜索栏
	if scrollingUp && atTop && a.controlsCollapsed {
		if a.topReachedAt.IsZero() || ev.Time.Sub(a.topReachedAt) > topSettleDuration {
			a.upScrollAccum += absDY
			if a.upScrollAccum >= searchRevealThreshold {
				a.controlsCollapsed = false
				a.upScrollAccum = 0
				if offset > 0 {
					a.listScroll.Y = 0
				}
			}
			return true
		}
		a.upScrollAccum = 0
		return false
	}

	if !scrollingUp || !atTop {
		a.upScrollAccum = 0
	}
	return false
}

func (a *clipApp) renderCardActionBtn(c *ui.Context, icon *ui.SVG, label string, active bool, onClick func()) {
	t := c.Theme()
	btn := ui.ButtonBase(c).Size(cardActionSize, cardActionSize).Center().Radius(cardActionSize / 2).Label(label).Tooltip(label)
	bg := t.Surface
	border := t.Border
	fg := t.Text.Mix(t.TextMuted, 0.55)
	if active {
		bg = t.Accent
		border = t.Accent
		fg = t.AccentText
	} else if btn.Hovered() {
		bg = t.SurfaceHover
		border = t.Border.Mix(t.TextMuted, 0.35)
		fg = t.Text
	}
	btn.Background(bg).Border(1, border).Transition(colorTransition).
		OnClick(onClick).
		Children(func() {
			ui.Icon(c, icon).FontSize(13).TextColor(fg)
		})
}

func (a *clipApp) toggleSensitiveReveal(id int64) {
	if a.revealedSensitive == nil {
		a.revealedSensitive = make(map[int64]bool)
	}
	if a.revealedSensitive[id] {
		delete(a.revealedSensitive, id)
	} else {
		a.revealedSensitive[id] = true
	}
}

func (a *clipApp) renderSensitiveImageMask(c *ui.Context, t *ui.Theme, msgs locales.Messages) {
	h := float32(72)
	if a.settings.CompactMode {
		h = 34
	}
	ui.Row(c).FillWidth().Height(h).Padding(0, 12).Radius(radiusControl).
		Background(t.SurfaceHover).Border(1, t.Border).
		AlignItems(ui.Center).Justify(ui.Center).Gap(6).Children(func() {
		ui.Icon(c, iconEyeOff).FontSize(15).TextColor(t.TextMuted)
		ui.Text(c, msgs.CardSensitiveHidden()).FontSize(12.5).TextColor(t.TextMuted).SingleLine()
	})
}

func (a *clipApp) cardContentWidth() float32 {
	// 扣除列表左右 padding(32) + 卡片左右 padding(24) + 卡片与图片边框(4)
	return max(120, a.panelWidth()-60)
}

func (a *clipApp) renderImagePreview(c *ui.Context, item store.ClipItem) {
	t := c.Theme()
	maxW, maxH := a.cardContentWidth(), float32(100)
	if a.settings.CompactMode {
		maxW, maxH = min(120, maxW), 38
	}
	if bmp := a.getOrLoadThumb(item.ImagePath); bmp != nil {
		iw, ih := bmp.Size()
		// 横宽图使用 FillWidth + Cover 铺满卡片内容宽度，避免 1px 边框或最小高度限制导致左右/上下留白
		if !a.settings.CompactMode && iw > 0 && ih > 0 && float32(iw) >= maxW && float32(iw)*maxH/float32(ih) >= maxW {
			estH := max(float32(28), maxW*float32(ih)/float32(iw))
			ui.Image(c, bmp).FillWidth().MinHeight(28).MaxHeight(maxH).Fit(ui.Cover).
				Radius(imagePreviewRadius(estH)).Border(1, t.Border)
			return
		}
		w, h := fitImageSize(bmp, maxW, maxH)
		ui.Image(c, bmp).Size(w, h).Fit(ui.Cover).Radius(imagePreviewRadius(min(w, h))).
			Border(1, t.Border)
		return
	}
	// 异步加载完成前保持占位高度，防止列表发生跳动
	ui.Box(c).Size(min(100, maxW), maxH).Radius(radiusControl).Background(t.SurfaceHover)
}

func imagePreviewRadius(shortSide float32) float32 {
	if shortSide >= 56 {
		return radiusControl
	}
	return max(radiusMicro, min(radiusControl, shortSide*0.25))
}

func fitImageSize(bmp *ui.Bitmap, maxW, maxH float32) (float32, float32) {
	iw, ih := bmp.Size()
	if iw <= 0 || ih <= 0 {
		return maxH, maxH
	}
	w, h := float32(iw), float32(ih)
	scale := min(maxH/h, maxW/w)
	if scale > 1 {
		scale = 1
	}
	return max(24, w*scale), max(24, h*scale)
}

func (a *clipApp) renderFilesPreview(c *ui.Context, item store.ClipItem) {
	t := c.Theme()
	maxShow := min(3, len(item.Files))
	if a.settings.CompactMode {
		maxShow = min(1, len(item.Files))
	}
	ui.Column(c).Gap(3).Children(func() {
		for i := 0; i < maxShow; i++ {
			p := item.Files[i]
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, filepath.Base(p)).FontSize(14.5).TextColor(t.Text).SingleLine()
				ui.Text(c, filepath.Dir(p)).FontSize(12.5).TextColor(t.TextMuted).SingleLine().Grow(1)
			})
		}
		if len(item.Files) > maxShow {
			ui.Text(c, locales.In(c.Locale()).CardFilesMore(len(item.Files))).FontSize(12.5).TextColor(t.TextMuted)
		}
	})
}

func (a *clipApp) getRichPreviewSpans(item store.ClipItem, t *ui.Theme) []ui.Span {
	if item.HTML == "" {
		return nil
	}
	if a.richSpans == nil || a.richSpansDark != t.Dark || len(a.richSpans) >= maxRichSpanCacheEntries {
		a.richSpans = make(map[int64][]ui.Span)
		a.richSpansDark = t.Dark
	}
	if spans, ok := a.richSpans[item.ID]; ok {
		return spans
	}
	spans := parseHTMLPreviewSpans(item.HTML, t)
	a.richSpans[item.ID] = spans
	return spans
}

const (
	maxThumbCacheCount    = 12
	maxThumbCacheBytes    = 8 << 20
	maxAppIconCacheBytes  = 2 << 20
	thumbMaxWidth         = 850
	thumbMaxHeight        = 250
	minThumbPreloadMargin = 1
	maxThumbPreloadMargin = 2
)

func loadThumbFile(path string) (*ui.Bitmap, error) {
	return loadOrCreateThumbBitmap(path, store.ThumbPath(path), thumbMaxWidth, thumbMaxHeight)
}

func (a *clipApp) getOrLoadThumb(path string) *ui.Bitmap {
	a.thumbs.markActive(path)
	return a.thumbs.getBitmap(a.panel, path, maxThumbCacheCount, func() (*ui.Bitmap, error) {
		return loadThumbFile(path)
	})
}

func (a *clipApp) preloadThumb(path string) {
	a.thumbs.preloadBitmap(a.panel, path, maxThumbCacheCount, func() (*ui.Bitmap, error) {
		return loadThumbFile(path)
	})
}

// syncThumbWindow preloads thumbnails within [firstVisible - margin, lastVisible + margin],
// evicts hidden sensitive entries immediately, cancels queued decodes outside the window,
// and caps total retained thumbnails by LRU and byte budget.
func (a *clipApp) syncThumbWindow(items []store.ClipItem, firstVisible, lastVisible int) {
	if a.thumbs == nil {
		return
	}
	if len(items) == 0 {
		a.thumbs.setActiveKeys(map[string]struct{}{})
		return
	}
	if firstVisible > lastVisible || lastVisible < 0 || firstVisible >= len(items) {
		return
	}
	firstVisible = max(0, firstVisible)
	lastVisible = min(len(items)-1, lastVisible)
	pageSize := max(1, lastVisible-firstVisible+1)
	margin := min(maxThumbPreloadMargin, max(minThumbPreloadMargin, pageSize/2))
	windowStart := max(0, firstVisible-margin)
	windowEnd := min(len(items)-1, lastVisible+margin)

	activeKeys := make(map[string]struct{}, windowEnd-windowStart+1)
	for i := windowStart; i <= windowEnd; i++ {
		item := items[i]
		if item.Kind != store.KindImage || item.ImagePath == "" {
			continue
		}
		if store.HasSensitiveTag(item.Tags) && !a.revealedSensitive[item.ID] {
			a.thumbs.evict(item.ImagePath)
			continue
		}
		activeKeys[item.ImagePath] = struct{}{}
	}
	touchActive := func(i int) {
		item := items[i]
		if item.Kind != store.KindImage || item.ImagePath == "" {
			return
		}
		if _, active := activeKeys[item.ImagePath]; active {
			a.thumbs.touchIfPresent(item.ImagePath)
		}
	}
	// Touch LRU from coldest active tier to hottest: preload margin -> list overscan -> core visible.
	// When anchored at row 0 (including initial unmeasured frames), touch core rows in reverse so row 0 stays newest.
	for i := windowStart; i < firstVisible; i++ {
		touchActive(i)
	}
	for i := lastVisible + 1; i <= windowEnd; i++ {
		touchActive(i)
	}
	coreStart, coreEnd := firstVisible, lastVisible
	if visFirst, visLast := a.list.Visible(); visLast >= visFirst && visFirst < len(items) {
		overlapStart := max(firstVisible, visFirst)
		overlapEnd := min(lastVisible, visLast)
		if overlapStart <= overlapEnd {
			coreStart, coreEnd = overlapStart, overlapEnd
			for i := firstVisible; i <= lastVisible; i++ {
				if i < coreStart || i > coreEnd {
					touchActive(i)
				}
			}
		}
	}
	if coreStart == 0 {
		for i := coreEnd; i >= coreStart; i-- {
			touchActive(i)
		}
	} else {
		for i := coreStart; i <= coreEnd; i++ {
			touchActive(i)
		}
	}
	a.thumbs.setActiveKeys(activeKeys)
	pruned := false
	if a.thumbs.len() > maxThumbCacheCount {
		pruned = a.thumbs.pruneTo(maxThumbCacheCount) || pruned
	}
	pruned = a.thumbs.pruneToBytes(maxThumbCacheBytes) || pruned
	for _, path := range a.selectThumbPreloads(items, firstVisible, lastVisible, windowStart, windowEnd, activeKeys, pruned) {
		a.preloadThumb(path)
	}
}

// selectThumbPreloads picks nearby off-screen thumbnails from nearest to farthest
// only while the active window's projected count and byte budget have headroom,
// allowing cold out-of-window entries to be evicted while preventing active thrash.
func (a *clipApp) selectThumbPreloads(items []store.ClipItem, firstVisible, lastVisible, windowStart, windowEnd int, activeKeys map[string]struct{}, pruned bool) []string {
	if pruned || a.thumbs == nil {
		return nil
	}
	activeCached, activeBytes, activeLoading := a.thumbs.activeStats(activeKeys)
	projectedCount := activeCached + activeLoading
	if projectedCount >= maxThumbCacheCount {
		return nil
	}
	const defaultEstThumbBytes int64 = int64(thumbMaxWidth) * int64(thumbMaxHeight) * 4
	estBytes := defaultEstThumbBytes
	if activeCached > 0 && activeBytes > 0 {
		estBytes = max(activeBytes/int64(activeCached), 256<<10)
	} else if n := a.thumbs.len(); n > 0 && a.thumbs.bytes() > 0 {
		estBytes = max(a.thumbs.bytes()/int64(n), 256<<10)
	}
	projectedBytes := activeBytes + int64(activeLoading)*estBytes
	if projectedBytes+estBytes > maxThumbCacheBytes {
		return nil
	}

	var preloadPaths []string
	seen := make(map[string]struct{})
	tryAdd := func(i int) bool {
		if i < windowStart || i > windowEnd {
			return false
		}
		item := items[i]
		p := item.ImagePath
		if p == "" || item.Kind != store.KindImage {
			return false
		}
		if _, active := activeKeys[p]; !active {
			return false
		}
		if _, dup := seen[p]; dup || a.thumbs.hasOrPending(p) {
			return false
		}
		if projectedCount >= maxThumbCacheCount || projectedBytes+estBytes > maxThumbCacheBytes {
			return true
		}
		seen[p] = struct{}{}
		preloadPaths = append(preloadPaths, p)
		projectedCount++
		projectedBytes += estBytes
		return false
	}

	maxDist := max(firstVisible-windowStart, windowEnd-lastVisible)
	for d := 1; d <= maxDist; d++ {
		if tryAdd(lastVisible + d) {
			break
		}
		if tryAdd(firstVisible - d) {
			break
		}
	}
	return preloadPaths
}

func (a *clipApp) getOrLoadAppIcon(path string) *ui.Bitmap {
	if path == "" || !mygo.App.IsReady() {
		return nil
	}
	return a.appIcons.get(a.panel, path, 64, func() ([]byte, error) {
		return mygo.Shell.FileIcon(path, 32)
	})
}

func kindMeta(msgs locales.Messages, kind store.ClipKind) (*ui.SVG, string) {
	switch kind {
	case store.KindRichText:
		return iconRichText, msgs.TabRichText()
	case store.KindURL:
		return iconLink, msgs.TabURL()
	case store.KindCode:
		return iconCode, msgs.TabCode()
	case store.KindImage:
		return iconImage, msgs.TabImage()
	case store.KindFile:
		return iconFile, msgs.TabFile()
	default:
		return iconText, msgs.TabText()
	}
}

func relativeTime(msgs locales.Messages, t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return msgs.TimeJustNow()
	case d < time.Hour:
		return msgs.TimeMinutesAgo(int(d.Minutes()))
	case d < 24*time.Hour:
		return msgs.TimeHoursAgo(int(d.Hours()))
	default:
		return msgs.TimeDaysAgo(int(d.Hours() / 24))
	}
}
