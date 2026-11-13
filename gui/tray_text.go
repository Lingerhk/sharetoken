package main

import "strings"

// trayText is the native menu's text in one language. The page's dictionaries
// (internal/client/ui/i18n/client) are not available here, because the native menu has
// to work when the page cannot load. Connected, Connecting and Retrying repeat
// the page's own words, and a test compares them. {app} is the app's name and
// {version} the new release's.
type trayText struct {
	SignedOut, Expired, Connecting, Retrying, Connected, ServiceDown, Open, Quit string
	// The menu item and the system notification about a new release.
	UpdateAvailable, UpdateRequired, NotifyAvailable, NotifyRequired string
	// The lines under the status: the relay in use ({name}) without and with
	// its latency ({ms}), the models on sale ({n}) and the balance ({n}).
	Relay, RelayRTT, Sharing, Balance string
}

func (t trayText) fill(s string) string { return strings.ReplaceAll(s, "{app}", windowTitle()) }

var trayTexts = map[string]trayText{
	"en": {"Not signed in", "Sign-in expired", "Connecting to the network…", "Can't reach the network. Retrying", "Connected", "The background service is not running", "Open {app}", "Quit {app}", "Update available: {version}", "Upgrade required: {version}", "Version {version} is available. Open {app} to download it.", "This version of {app} is no longer supported. Open {app} to upgrade.",
		"Relay: {name}", "Relay: {name} · {ms} ms", "Sharing · models on sale: {n}", "Available credit: {n}"},
	"zh-CN": {"未登录", "登录已失效", "正在连接网络…", "暂时连不上网络，正在重试", "已连接", "后台服务未运行", "打开 {app}", "退出 {app}", "有新版本 {version}", "需要升级到 {version}", "新版本 {version} 已发布，打开 {app} 下载。", "这个版本的 {app} 已不再受支持，请打开 {app} 升级。",
		"中继：{name}", "中继：{name} · {ms} ms", "正在共享 · 在售模型：{n}", "可用 credit：{n}"},
}
