package main

import (
	"context"
	"log"
	"sync"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// desktopNotifier shows system notifications through Wails. It is set up at the
// first one, so the system's permission prompt (macOS) appears only when there
// is something to say. A click on a notification brings the window back.
// Where notifications are not possible, such as a macOS app without a bundle,
// it shows nothing and the tray item and the page still tell the user.
type desktopNotifier struct {
	ctx  context.Context
	show func()

	once sync.Once
	ok   bool
}

func (n *desktopNotifier) send(title, body string) bool {
	n.once.Do(func() {
		if err := runtime.InitializeNotifications(n.ctx); err != nil {
			log.Printf("sharetoken-gui: notifications: %v", err)
			return
		}
		runtime.OnNotificationResponse(n.ctx, func(runtime.NotificationResult) { n.show() })
		n.ok = true
	})
	if !n.ok {
		return false
	}
	if granted, err := runtime.RequestNotificationAuthorization(n.ctx); err != nil || !granted {
		log.Printf("sharetoken-gui: notifications not allowed (%v)", err)
		return false
	}
	if err := runtime.SendNotification(n.ctx, runtime.NotificationOptions{ID: "sharetoken-update", Title: title, Body: body}); err != nil {
		log.Printf("sharetoken-gui: notification: %v", err)
		return false
	}
	return true
}
