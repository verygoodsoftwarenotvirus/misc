package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/playwright-community/playwright-go"
)

// PageFunc drives a single Playwright page.
type PageFunc func(page playwright.Page) error

// RunWithChromiumPageAndPermissions launches Chromium, creates a new browser context with permissions,
// creates a page, and runs fn. Resources are cleaned up automatically.
//
// Note: clipboard access may require `clipboard-read` / `clipboard-write` permissions.
func RunWithChromiumPageAndPermissions(
	ctx context.Context,
	launchOptions playwright.BrowserTypeLaunchOptions,
	permissions []string,
	fn PageFunc,
) (returnErr error) {
	if fn == nil {
		return fmt.Errorf("fn is required")
	}

	pw, err := playwright.Run()
	if err != nil {
		return fmt.Errorf("playwright.Run: %w", err)
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch(launchOptions)
	if err != nil {
		return fmt.Errorf("chromium.Launch: %w", err)
	}

	// Create an isolated context for this run so we can control permissions (clipboard).
	bctx, err := browser.NewContext(playwright.BrowserNewContextOptions{
		Permissions: permissions,
	})
	if err != nil {
		_ = browser.Close()
		return fmt.Errorf("browser.NewContext: %w", err)
	}
	defer func() { _ = bctx.Close() }()

	page, err := bctx.NewPage()
	if err != nil {
		_ = browser.Close()
		return fmt.Errorf("context.NewPage: %w", err)
	}

	var closeOnce sync.Once
	closeAll := func() {
		closeOnce.Do(func() {
			_ = bctx.Close()
			_ = browser.Close()
		})
	}
	defer closeAll()

	// If the caller cancels the context, try to close the context to unblock the callback.
	if ctx != nil {
		go func() {
			<-ctx.Done()
			closeAll()
		}()
	}

	returnErr = fn(page)
	return returnErr
}

func RunHeadlessChromiumPageWithClipboardPermissions(ctx context.Context, fn PageFunc) error {
	return RunWithChromiumPageAndPermissions(ctx, playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	}, []string{
		"clipboard-read",
		"clipboard-write",
	}, fn)
}

const (
	w2gBaseURL = "https://w2g.tv/en"

	// These selectors are straight from the `data-w2g="..."` attributes on the page.
	w2gCreateRoomSel   = `[data-w2g="['createRoom', ['event', 'click']]"]`
	w2gIntroNickSel    = `[data-w2g="['introNick', 'value']"]`
	w2gShowIntLinkSel  = `[data-w2g="['showIntLink', 'ifnot']"]`
	w2gCopyInviteSel   = `[data-w2g="['copyInvite', ['event', 'mousedown']]"]`
	w2gCloseSuccessKey = "Close success modal"
)

// GetW2GInviteFromClipboard drives the W2G flow described by you and returns the clipboard text.
func GetW2GInviteFromClipboard(ctx context.Context, page playwright.Page) (string, error) {
	tryDismissAriaLabelOn := func(p playwright.Page, ariaLabel string, timeout time.Duration) {
		selector := fmt.Sprintf(`[aria-label=%q]`, ariaLabel)
		loc := p.Locator(selector)
		if err := loc.WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateVisible,
			Timeout: playwright.Float(float64(timeout.Milliseconds())),
		}); err != nil {
			return
		}
		_ = loc.Click(playwright.LocatorClickOptions{
			Force: playwright.Bool(true),
		})
	}

	// Start at the W2G intro flow.
	if _, err := page.Goto(w2gBaseURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateLoad,
	}); err != nil {
		return "", fmt.Errorf("goto %q: %w", w2gBaseURL, err)
	}

	// Dismiss cookie/consent modal if it appears.
	tryDismissAriaLabelOn(page, "Close", 6*time.Second)

	// Click "create room".
	workPage := page
	createRoom := page.Locator(w2gCreateRoomSel)
	if err := createRoom.WaitFor(); err != nil {
		return "", fmt.Errorf("wait for createRoom element: %w", err)
	}

	// This click can either navigate the current page or open a popup, so we attempt to capture a popup (best-effort).
	if popupPage, err := page.ExpectPopup(func() error {
		return createRoom.Click(playwright.LocatorClickOptions{Force: playwright.Bool(true)})
	}, playwright.PageExpectPopupOptions{
		Predicate: func(playwright.Page) bool { return true },
		Timeout:   playwright.Float(5_000),
	}); err == nil {
		workPage = popupPage
		// Some flows can show cookie/consent again on the popup.
		tryDismissAriaLabelOn(workPage, "Close", 2*time.Second)
	}

	// Fill nickname after navigation/popup.
	nickInput := workPage.Locator(w2gIntroNickSel)
	if err := nickInput.WaitFor(); err != nil {
		return "", fmt.Errorf("wait for introNick input: %w", err)
	}
	if err := nickInput.Fill("w2gbot"); err != nil {
		return "", fmt.Errorf("fill introNick input: %w", err)
	}

	// Click "show invite link".
	showIntLink := workPage.Locator(w2gShowIntLinkSel)
	if err := showIntLink.WaitFor(); err != nil {
		return "", fmt.Errorf("wait for showIntLink element: %w", err)
	}
	if err := showIntLink.Click(playwright.LocatorClickOptions{Force: playwright.Bool(true)}); err != nil {
		return "", fmt.Errorf("click showIntLink element: %w", err)
	}

	// Mousedown the "copy invite" control.
	copyInvite := workPage.Locator(w2gCopyInviteSel)
	if err := copyInvite.WaitFor(); err != nil {
		return "", fmt.Errorf("wait for copyInvite element: %w", err)
	}
	if err := copyInvite.DispatchEvent("mousedown", nil); err != nil {
		return "", fmt.Errorf("dispatch mousedown on copyInvite element: %w", err)
	}

	// Success modal is best-effort; it shouldn't break clipboard reads, but dismiss it anyway.
	tryDismissAriaLabelOn(workPage, w2gCloseSuccessKey, 2*time.Second)

	// Read clipboard text (retry briefly for propagation).
	var lastClipboard string
	for i := 0; i < 25; i++ {
		if ctx != nil {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}

		v, err := workPage.Evaluate("async () => await navigator.clipboard.readText()")
		if err == nil {
			if s, ok := v.(string); ok {
				lastClipboard = s
				if s != "" {
					return s, nil
				}
			}
		}

		time.Sleep(150 * time.Millisecond)
	}

	// Best-effort debug aid.
	copyText, _ := copyInvite.TextContent()
	if lastClipboard == "" && copyText != "" {
		// Clipboard read can be blocked in headless contexts.
		// In that case, fall back to the invite text visible in the UI.
		return copyText, nil
	}
	return "", fmt.Errorf(
		"clipboard readText returned empty/failed after copy action; lastClipboard=%q copyElementText=%q",
		lastClipboard,
		copyText,
	)
}

func postToDiscord(link string) error {
	token := os.Getenv("DISCORD_BOT_TOKEN")
	channelID := os.Getenv("DISCORD_CHANNEL_ID")
	if token == "" || channelID == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN and DISCORD_CHANNEL_ID must be set")
	}
	if !strings.HasPrefix(token, "Bot ") {
		// discordgo expects tokens prefixed with "Bot " (unless you pass a full token explicitly).
		token = "Bot " + token
	}

	dg, err := discordgo.New(token)
	if err != nil {
		return fmt.Errorf("creating Discord session: %w", err)
	}

	if _, err = dg.ChannelMessageSend(channelID, link); err != nil {
		return fmt.Errorf("sending link: %w", err)
	}

	return nil
}

func main() {
	ctx := context.Background()

	var invite string
	if err := RunHeadlessChromiumPageWithClipboardPermissions(ctx, func(page playwright.Page) error {
		inviteText, err := GetW2GInviteFromClipboard(ctx, page)
		if err != nil {
			return err
		}
		invite = inviteText
		return nil
	}); err != nil {
		log.Fatal(err)
	}

	fmt.Println(invite)

	// Optional: post to Discord if configured.
	if os.Getenv("DISCORD_BOT_TOKEN") != "" && os.Getenv("DISCORD_CHANNEL_ID") != "" {
		if err := postToDiscord(invite); err != nil {
			log.Fatal(err)
		}
	}
}
