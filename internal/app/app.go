package app

import (
	"log"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mytiez/internal/store"
	"mytiez/internal/sys"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

var globalShortcuts = []string{"Alt+V", "Ctrl+Shift+V", "Alt+Shift+V"}

const (
	// warmThumbCacheLimit keeps up to 4 recently viewed thumbnails (~3-4 MB at
	// 850x250) intact on panel hide when process memory is healthy so MyGo's D3D11
	// renderer reuses existing textures instead of creating new IDs on every show.
	warmThumbCacheLimit = 4
	// heapReleaseDelay returns idle Go heap spans to the OS shortly after hiding
	// without evicting warm bitmaps, preventing working set from creeping up.
	heapReleaseDelay = 5 * time.Second
	// idleUnloadDelay unloads hidden UI caches and item slices after the panel
	// stays hidden for 1 minute (inspired by CopyQ's tab expiration), allowing
	// long-running background instances to settle at minimal RAM usage.
	idleUnloadDelay = 1 * time.Minute
)

type clipApp struct {
	iconPNG           []byte
	store             *store.Store
	settings          store.Settings
	panel             *mygo.Window
	tray              *mygo.Tray
	trayOpenItem      *mygo.MenuItem
	shortcut          string
	stopWatch         func()
	stopTrack         func()
	items             []store.ClipItem
	searchHits        []store.ClipItem
	query             string
	filterIdx         int // 0: 全部, 1: 文本, 2: 图片, 3: 文件, 4: 链接, 5: 代码, 6: 富文本
	selected          int
	list              ui.ListState
	listHandle        ui.Handle
	listScroll        ui.ScrollState
	settingsScroll    ui.ScrollState
	searchHandle      ui.Handle
	editingTagsID     int64
	tagInput          string
	tagInputHandle    ui.Handle
	tagFocusPending   bool
	allTags           []string
	editingTagOrder   []string
	loadedRevision    uint64
	loadedTagRevision uint64
	loadedQuery       string
	loadedKind        store.ClipKind
	loadedLimit       int

	thumbs               *bitmapCache
	appIcons             *bitmapCache
	richSpans            map[int64][]ui.Span
	richSpansDark        bool
	revealedSensitive    map[int64]bool
	tempFiles            map[string]struct{}
	fileMenuOpen         map[int64]*bool
	fileMenuStatID       int64
	fileMenuStatAt       time.Time
	fileMenuStatPaths    []string
	fileMenuExists       map[string]bool
	cardMenuRootHandle   ui.Handle
	closeCardContextMenu bool

	ignoreHash         string
	ignoreUntil        time.Time
	ignoreClipSeq      uint32
	winPinned          bool
	panelNoActivate    bool
	routedSearchActive bool
	routedCaretPending bool
	stopOutsideWatch   func()
	stopKeyRouter      func()
	edgeDock           dockPosition
	edgeDockStop       chan struct{}
	lastShowAt         time.Time
	lastOutsideHide    time.Time
	pickingApp         bool
	restartingAdmin    bool
	quitting           bool
	resizeTimer        *time.Timer
	searchTimer        *time.Timer
	persistTimer       *time.Timer
	persistPending     bool
	updateTimer        *time.Timer
	heapReleaseTimer   *time.Timer
	heapReleaseGen     uint64
	idleUnloadTimer    *time.Timer
	idleUnloadGen      uint64
	updateChecking     bool
	updateInstalling   bool
	updateProgress     int
	updateReady        bool
	availableUpdate    *mygo.Update

	showSettings        bool
	confirmClearOpen    bool
	controlsCollapsed   bool
	atTop               bool
	topReachedAt        time.Time
	upScrollAccum       float32
	listResetPending    bool
	snapNextFrame       bool
	hasShownOnce        bool
	expanded            settingsGroupExpanded
	accentPicker        *accentPickerState
	fontQuery           string
	systemFonts         []ui.FontFamily
	systemFontsLoaded   bool
	confirmAction       string
	recordingHotkey     bool
	recordingTarget     string // "" | "global" | "plain_paste"
	hotkeyFocusPending  bool
	hotkeyMods          ui.Modifiers
	hotkeyHandle        ui.Handle
	plainShortcutHandle ui.Handle
	pendingToast        string
	memTrimming         atomic.Bool
	saveMu              sync.Mutex
	saveQueue           []func() (string, error)
	saveRunning         bool
	backupDialogOpen    bool
	backupDialogMode    string // "export" | "import"
	backupPassword      string
	backupImportPath    string
	backupBusy          bool
	backupPassHandle    ui.Handle
}

func newClipApp(st *store.Store, iconPNG []byte) *clipApp {
	cfg := store.DefaultSettings()
	if st != nil {
		if loaded, err := st.LoadSettings(); err == nil {
			cfg = loaded
		}
	}
	a := &clipApp{
		iconPNG:         iconPNG,
		store:           st,
		settings:        cfg,
		selected:        -1,
		atTop:           true,
		winPinned:       !cfg.AutoHideOnBlur,
		panelNoActivate: cfg.NoFocusMode,
		thumbs:          newBitmapCacheWithBudget(1, maxThumbCacheBytes),
		appIcons:        newBitmapCacheWithBudget(2, maxAppIconCacheBytes),
	}
	a.syncPlainPasteShortcut()
	return a
}

// Run starts the clipboard manager application.
func Run(iconPNG []byte) {
	sys.PrepareElevatedRestart()
	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(50)
	}
	if os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(96 << 20)
	}
	if !mygo.App.RequestSingleInstanceLock() {
		return
	}

	st, err := store.OpenStore("")
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	app := newClipApp(st, iconPNG)
	mygo.App.OnSecondInstance(func([]string, string) {
		app.showPanel()
	})
	mygo.App.WhenReady(app.ready)

	if err := mygo.App.Run(); err != nil {
		log.Printf("run app: %v", err)
	}
}

func (a *clipApp) toggleWindowPin() {
	a.applyAutoHide(a.winPinned)
	a.persistSettings()
}

func (a *clipApp) applyAutoHide(autoHide bool) {
	a.settings.AutoHideOnBlur = autoHide
	a.winPinned = !autoHide
	if a.panel != nil && mygo.App.IsReady() {
		a.panel.SetAlwaysOnTop(a.winPinned || a.panelNoActivate)
	}
	a.syncOutsideClickWatch()
}

func (a *clipApp) stopHeapReleaseTimer() {
	a.heapReleaseGen++
	if a.heapReleaseTimer != nil {
		a.heapReleaseTimer.Stop()
		a.heapReleaseTimer = nil
	}
}

func (a *clipApp) scheduleHeapRelease() {
	a.stopHeapReleaseTimer()
	if a.panel == nil || a.quitting {
		return
	}
	panel := a.panel
	gen := a.heapReleaseGen
	a.heapReleaseTimer = time.AfterFunc(heapReleaseDelay, func() {
		panel.Update(func() {
			a.releaseHiddenHeapForGen(gen)
		})
	})
}

func (a *clipApp) releaseHiddenHeapForGen(gen uint64) {
	if gen != a.heapReleaseGen {
		return
	}
	a.releaseHiddenHeap()
}

// releaseHiddenHeap returns idle Go heap spans to the OS after the panel stays
// hidden briefly, without clearing warm bitmaps so D3D11 textures stay reusable.
func (a *clipApp) releaseHiddenHeap() {
	a.stopHeapReleaseTimer()
	if a.quitting || (a.panel != nil && a.panel.IsVisible()) {
		return
	}
	if a.memTrimming.CompareAndSwap(false, true) {
		go func() {
			defer a.memTrimming.Store(false)
			debug.FreeOSMemory()
		}()
	}
}

// releaseImagePasteMemory returns transient full-resolution image decode
// buffers to the OS immediately after writing an image to the clipboard,
// including when the window is pinned or in click-copy-only mode.
func (a *clipApp) releaseImagePasteMemory() {
	if a.memTrimming.CompareAndSwap(false, true) {
		go func() {
			defer a.memTrimming.Store(false)
			debug.FreeOSMemory()
			sys.TrimIdleMemory()
		}()
	}
}

func (a *clipApp) stopIdleUnloadTimer() {
	a.idleUnloadGen++
	if a.idleUnloadTimer != nil {
		a.idleUnloadTimer.Stop()
		a.idleUnloadTimer = nil
	}
}

func (a *clipApp) scheduleIdleUnload() {
	a.stopIdleUnloadTimer()
	if a.panel == nil || a.quitting {
		return
	}
	panel := a.panel
	gen := a.idleUnloadGen
	a.idleUnloadTimer = time.AfterFunc(idleUnloadDelay, func() {
		panel.Update(func() {
			a.unloadHiddenIdleStateForGen(gen)
		})
	})
}

func (a *clipApp) unloadHiddenIdleStateForGen(gen uint64) {
	if gen != a.idleUnloadGen {
		return
	}
	a.unloadHiddenIdleState()
}

// unloadHiddenIdleState releases UI lists and caches after the panel stays hidden
// for idleUnloadDelay, similar to CopyQ's expired tab unloading. The next showPanel
// reloads fresh summaries seamlessly via reloadIfStale.
func (a *clipApp) unloadHiddenIdleState() {
	a.stopIdleUnloadTimer()
	if a.quitting || (a.panel != nil && a.panel.IsVisible()) {
		return
	}
	if a.thumbs != nil {
		a.thumbs.clear()
	}
	if a.appIcons != nil {
		a.appIcons.clear()
	}
	a.richSpans = nil
	a.searchHits = nil
	a.items = nil
	a.selected = -1
	a.loadedRevision = 0
	a.fileMenuStatID = 0
	a.fileMenuStatPaths = nil
	a.fileMenuExists = nil
	a.fileMenuOpen = nil

	if a.memTrimming.CompareAndSwap(false, true) {
		st := a.store
		go func() {
			defer a.memTrimming.Store(false)
			if st != nil {
				st.ShrinkMemory()
			}
			debug.FreeOSMemory()
			sys.TrimIdleMemory()
		}()
	}
}

// releaseHiddenMemory prunes caches and trims OS working set pages as needed.
// Under the 75 MB working set threshold, up to warmThumbCacheLimit (4) recently
// viewed thumbnails and richSpans are preserved on hide to avoid wake-up CPU spikes.
func (a *clipApp) releaseHiddenMemory() {
	a.fileMenuStatID = 0
	a.fileMenuStatPaths = nil
	a.fileMenuExists = nil
	if strings.TrimSpace(a.query) == "" {
		a.searchHits = nil
	}

	overMemory := sys.WorkingSetBytes() > sys.MemoryTrimThresholdBytes

	if a.thumbs != nil {
		if overMemory {
			a.thumbs.clear()
		} else {
			a.thumbs.setActiveKeys(map[string]struct{}{})
			if a.thumbs.len() > warmThumbCacheLimit {
				a.thumbs.pruneTo(warmThumbCacheLimit)
			}
		}
	}
	if overMemory && a.appIcons != nil {
		a.appIcons.clear()
	}
	if overMemory && len(a.richSpans) > 0 {
		a.richSpans = nil
	}

	if !overMemory {
		return
	}
	if mygo.App.IsReady() && a.memTrimming.CompareAndSwap(false, true) {
		st := a.store
		go func() {
			defer a.memTrimming.Store(false)
			if st != nil {
				st.ShrinkMemory()
			}
			if sys.WorkingSetBytes() > sys.MemoryTrimThresholdBytes {
				debug.FreeOSMemory()
				sys.TrimMemory()
			}
		}()
	}
}

func (a *clipApp) panelWidth() float32 {
	winW := float32(a.settings.WindowWidth)
	if a.panel != nil {
		if w, _ := a.panel.Size(); w > 0 {
			winW = float32(w)
		}
	}
	if winW <= 0 {
		winW = panelWidth
	}
	return winW
}

func (a *clipApp) saveWindowSize() {
	if a.panel == nil {
		return
	}
	w, h := a.panel.Size()
	if w < minPanelWidth || h < minPanelHeight {
		return
	}
	if a.settings.WindowWidth == w && a.settings.WindowHeight == h {
		return
	}
	a.settings.WindowWidth = w
	a.settings.WindowHeight = h
	a.persistSettings()
}

func (a *clipApp) ready() {
	a.settings.AutoStart = mygo.App.OpenAtLogin()
	winW := max(minPanelWidth, a.settings.WindowWidth)
	winH := max(minPanelHeight, a.settings.WindowHeight)

	a.panel = mygo.NewWindow(mygo.WindowOptions{
		Title:           "MyTieZ",
		Panel:           true,
		AlwaysOnTop:     a.winPinned,
		Hidden:          true,
		Frameless:       true,
		Transparent:     true,
		BackgroundColor: "transparent",
		Vibrancy:        backdropVibrancy(a.settings.BackdropMode),
		CornerRadius:    22,
		Width:           winW,
		Height:          winH,
		MinWidth:        minPanelWidth,
		MinHeight:       minPanelHeight,
		StateKey:        "main-panel",
		Content:         ui.View(a.view),
	})

	a.panel.OnResize(func() {
		if a.resizeTimer != nil {
			a.resizeTimer.Reset(300 * time.Millisecond)
			return
		}
		a.resizeTimer = time.AfterFunc(300*time.Millisecond, func() {
			if a.panel != nil {
				a.panel.Update(a.saveWindowSize)
			}
		})
	})
	a.panel.OnClose(func(e *mygo.CloseEvent) {
		a.cancelHotkeyRecording()
		a.saveWindowSize()
		if a.quitting || !a.panel.IsVisible() {
			return
		}
		e.PreventDefault()
		a.hidePanel()
	})
	a.panel.OnBlur(func() {
		a.cancelHotkeyRecording()
		if !a.winPinned && !a.pickingApp && !(a.settings.EdgeDocking && a.isNearDockEdge()) {
			a.saveWindowSize()
			a.hidePanel()
		}
	})

	a.shortcut = a.settings.Hotkey
	if a.settings.OverrideWinV {
		a.applyWinVOverride()
	} else {
		a.registerFallbackHotkey()
	}

	a.setupTray()
	a.stopWatch = mygo.Clipboard.OnChanged(a.onClipboardChanged)
	a.startForegroundTracker()
	a.syncEdgeDocking()
	a.reload()
	a.updateTimer = time.AfterFunc(5*time.Second, func() {
		if a.panel != nil {
			a.panel.Update(func() { a.checkForUpdates(false) })
		}
	})

	if !mygo.App.WasOpenedAtLogin() {
		a.showPanel()
	}
}

func (a *clipApp) startForegroundTracker() {
	if a.stopTrack != nil {
		a.stopTrack()
		a.stopTrack = nil
	}
	stop, ok := sys.StartForegroundWatcher()
	if !ok {
		log.Printf("start foreground watcher: SetWinEventHook failed")
		return
	}
	a.stopTrack = stop
}
